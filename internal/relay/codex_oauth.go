package relay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/codexauth"
	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
	"golang.org/x/sync/singleflight"
)

const (
	codexProviderCacheLimit = 256
	codexRefreshBodyLimit   = 1 << 20
	codexRefreshUserAgent   = "octopus-codex-oauth"
	codexRequestRefreshLead = 3 * time.Minute
)

type codexProviderCacheEntry struct {
	signature [32]byte
	provider  oauth.TokenGetter
	accountID string
	lastUsed  uint64
}

var codexProviderCache = struct {
	sync.Mutex
	entries map[int]codexProviderCacheEntry
	clock   uint64
}{entries: make(map[int]codexProviderCacheEntry)}

type codexCredentialState struct {
	sync.Mutex
	channel  *dbmodel.Channel
	key      dbmodel.ChannelKey
	raw      string
	document *codexauth.Document
}

// codexTokenProvider intentionally refreshes with net/http directly. The
// generic AxonHub HTTP wrapper logs request objects at debug level, while an
// OAuth refresh request body contains the refresh token.
type codexTokenProvider struct {
	client   *http.Client
	tokenURL string
	state    *codexCredentialState
	sf       singleflight.Group
	mu       sync.RWMutex
	creds    *oauth.OAuthCredentials
}

type codexOAuthOutbound struct {
	*codex.OutboundTransformer
	accountID string
}

func (o *codexOAuthOutbound) TransformRequest(ctx context.Context, request *llm.Request) (*httpclient.Request, error) {
	outbound, err := o.OutboundTransformer.TransformRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	if outbound.Headers.Get("Chatgpt-Account-Id") == "" && o.accountID != "" {
		outbound.Headers.Set("Chatgpt-Account-Id", o.accountID)
	}
	return outbound, nil
}

func newChannelOutbound(channel *dbmodel.Channel, request *llm.Request, baseURL string, key dbmodel.ChannelKey) (transformer.Outbound, error) {
	if channel == nil {
		return nil, errors.New("channel is required")
	}
	if channel.Type == dbmodel.ChannelTypeOpenAICodex {
		return newCodexOAuthOutbound(channel, request, baseURL, key)
	}
	return newOutbound(channel.Type, request, baseURL, key.ChannelKey)
}

func newCodexOAuthOutbound(channel *dbmodel.Channel, request *llm.Request, baseURL string, key dbmodel.ChannelKey) (transformer.Outbound, error) {
	if request != nil && request.RequestType == llm.RequestTypeEmbedding {
		return nil, fmt.Errorf("channel type %s is not compatible with embedding request", dbmodel.ChannelTypeOpenAICodex)
	}
	if err := dbmodel.ValidateChannelAuthentication(dbmodel.ChannelTypeOpenAICodex, []dbmodel.BaseUrl{{URL: baseURL}}, []dbmodel.ChannelKey{key}); err != nil {
		return nil, fmt.Errorf("validate Codex OAuth outbound: %w", err)
	}
	provider, accountID, err := codexProviderForChannel(channel, key)
	if err != nil {
		return nil, err
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/#") + "#"
	outbound, err := codex.NewOutboundTransformer(codex.Params{TokenProvider: provider, BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("create Codex OAuth outbound: %w", err)
	}
	return &codexOAuthOutbound{OutboundTransformer: outbound, accountID: accountID}, nil
}

func codexProviderForChannel(channel *dbmodel.Channel, key dbmodel.ChannelKey) (oauth.TokenGetter, string, error) {
	if channel == nil {
		return nil, "", errors.New("codex OAuth channel is required")
	}
	document, err := codexauth.Parse(key.ChannelKey)
	if err != nil {
		return nil, "", fmt.Errorf("parse Codex OAuth credential: %w", err)
	}
	signature := codexProviderSignature(channel, key.ChannelKey)
	if key.ID > 0 {
		if cached, ok := getCodexProvider(key.ID, signature); ok {
			return cached.provider, cached.accountID, nil
		}
	}

	nativeClient, err := helper.ChannelHttpClient(channel)
	if err != nil {
		return nil, "", fmt.Errorf("create Codex OAuth refresh client: %w", err)
	}
	state := &codexCredentialState{channel: channel, key: key, raw: strings.TrimSpace(key.ChannelKey), document: document}
	provider := &codexTokenProvider{
		client:   nativeClient,
		tokenURL: codex.TokenURL,
		state:    state,
		creds:    document.Credentials(),
	}
	if key.ID > 0 {
		putCodexProvider(key.ID, codexProviderCacheEntry{signature: signature, provider: provider, accountID: document.AccountID()})
	}
	return provider, document.AccountID(), nil
}

func (p *codexTokenProvider) Get(ctx context.Context) (*oauth.OAuthCredentials, error) {
	return p.ensureFresh(ctx, codexRequestRefreshLead)
}

func (p *codexTokenProvider) ensureFresh(ctx context.Context, refreshBefore time.Duration) (*oauth.OAuthCredentials, error) {
	if p == nil {
		return nil, errors.New("codex OAuth token provider is unavailable")
	}
	current := p.credentials()
	if current == nil {
		return nil, errors.New("codex OAuth credentials are unavailable")
	}
	if !codexCredentialsNeedRefresh(current, time.Now(), refreshBefore) {
		return current, nil
	}

	value, err, _ := p.sf.Do("refresh", func() (any, error) {
		latest := p.credentials()
		if latest == nil {
			return nil, errors.New("codex OAuth credentials are unavailable")
		}
		if !codexCredentialsNeedRefresh(latest, time.Now(), refreshBefore) {
			return latest, nil
		}
		refreshed, err := p.refresh(ctx, latest)
		if err != nil {
			return nil, err
		}
		if p.state != nil {
			if err := p.state.persist(ctx, refreshed); err != nil {
				return nil, fmt.Errorf("persist refreshed Codex OAuth credential: %w", err)
			}
		}
		p.mu.Lock()
		p.creds = cloneOAuthCredentials(refreshed)
		p.mu.Unlock()
		return cloneOAuthCredentials(refreshed), nil
	})
	if err != nil {
		return nil, err
	}
	refreshed, ok := value.(*oauth.OAuthCredentials)
	if !ok {
		return nil, fmt.Errorf("codex OAuth refresh returned unexpected type %T", value)
	}
	return cloneOAuthCredentials(refreshed), nil
}

func codexCredentialsNeedRefresh(credentials *oauth.OAuthCredentials, now time.Time, refreshBefore time.Duration) bool {
	if credentials == nil || credentials.ExpiresAt.IsZero() {
		return true
	}
	if refreshBefore < 0 {
		refreshBefore = 0
	}
	return !now.Add(refreshBefore).Before(credentials.ExpiresAt)
}

func (p *codexTokenProvider) credentials() *oauth.OAuthCredentials {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneOAuthCredentials(p.creds)
}

func (p *codexTokenProvider) refresh(ctx context.Context, current *oauth.OAuthCredentials) (*oauth.OAuthCredentials, error) {
	if p.client == nil {
		return nil, errors.New("codex OAuth refresh client is unavailable")
	}
	if strings.TrimSpace(current.RefreshToken) == "" {
		return nil, errors.New("codex OAuth access token expired and refresh_token is unavailable")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current.RefreshToken},
		"client_id":     {codex.ClientID},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, errors.New("create Codex OAuth refresh request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", codexRefreshUserAgent)

	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send Codex OAuth refresh request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, codexRefreshBodyLimit+1))
	if err != nil {
		return nil, errors.New("read Codex OAuth refresh response")
	}
	if len(body) > codexRefreshBodyLimit {
		return nil, errors.New("codex OAuth refresh response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("codex OAuth refresh failed with HTTP status %d", response.StatusCode)
	}

	var token oauth.TokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, errors.New("decode Codex OAuth refresh response")
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, errors.New("codex OAuth refresh response has no access_token")
	}
	expiresAt := token.ExpiresAt()
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(time.Hour)
	}
	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "bearer"
	}
	scopes := strings.Fields(token.Scope)
	if len(scopes) == 0 {
		scopes = append([]string(nil), current.Scopes...)
	}
	return &oauth.OAuthCredentials{
		ClientID:     codex.ClientID,
		AccessToken:  strings.TrimSpace(token.AccessToken),
		RefreshToken: codexFirstNonEmpty(token.RefreshToken, current.RefreshToken),
		IDToken:      codexFirstNonEmpty(token.IDToken, current.IDToken),
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Scopes:       scopes,
	}, nil
}

func (s *codexCredentialState) persist(ctx context.Context, refreshed *oauth.OAuthCredentials) error {
	s.Lock()
	defer s.Unlock()
	if s.document == nil {
		return errors.New("codex OAuth credential document is unavailable")
	}
	updated, err := s.document.WithRefreshed(refreshed, time.Now())
	if err != nil {
		return err
	}
	parsed, err := codexauth.Parse(updated)
	if err != nil {
		return fmt.Errorf("validate refreshed Codex OAuth credential: %w", err)
	}
	if s.key.ID > 0 {
		if err := op.ChannelKeyCredentialReplace(ctx, s.key.ChannelID, s.key.ID, s.raw, updated); err != nil {
			return err
		}
	}
	s.raw = updated
	s.key.ChannelKey = updated
	s.document = parsed
	if s.key.ID > 0 && s.channel != nil {
		updateCodexProviderSignature(s.key.ID, codexProviderSignature(s.channel, updated))
	}
	return nil
}

func getCodexProvider(keyID int, signature [32]byte) (codexProviderCacheEntry, bool) {
	codexProviderCache.Lock()
	defer codexProviderCache.Unlock()
	entry, ok := codexProviderCache.entries[keyID]
	if !ok || entry.signature != signature {
		return codexProviderCacheEntry{}, false
	}
	codexProviderCache.clock++
	entry.lastUsed = codexProviderCache.clock
	codexProviderCache.entries[keyID] = entry
	return entry, true
}

func putCodexProvider(keyID int, entry codexProviderCacheEntry) {
	codexProviderCache.Lock()
	defer codexProviderCache.Unlock()
	codexProviderCache.clock++
	entry.lastUsed = codexProviderCache.clock
	codexProviderCache.entries[keyID] = entry
	if len(codexProviderCache.entries) <= codexProviderCacheLimit {
		return
	}
	oldestID := keyID
	oldest := entry.lastUsed
	for id, candidate := range codexProviderCache.entries {
		if candidate.lastUsed < oldest {
			oldestID = id
			oldest = candidate.lastUsed
		}
	}
	delete(codexProviderCache.entries, oldestID)
}

func updateCodexProviderSignature(keyID int, signature [32]byte) {
	codexProviderCache.Lock()
	defer codexProviderCache.Unlock()
	entry, ok := codexProviderCache.entries[keyID]
	if !ok {
		return
	}
	entry.signature = signature
	codexProviderCache.entries[keyID] = entry
}

func cloneOAuthCredentials(credentials *oauth.OAuthCredentials) *oauth.OAuthCredentials {
	if credentials == nil {
		return nil
	}
	clone := *credentials
	clone.Scopes = append([]string(nil), credentials.Scopes...)
	return &clone
}

func codexFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func codexProviderSignature(channel *dbmodel.Channel, credential string) [32]byte {
	proxyURL := ""
	if channel != nil && channel.ChannelProxy != nil {
		proxyURL = strings.TrimSpace(*channel.ChannelProxy)
	}
	proxyEnabled := channel != nil && channel.Proxy
	return sha256.Sum256([]byte(fmt.Sprintf("%t\x00%s\x00%s", proxyEnabled, proxyURL, strings.TrimSpace(credential))))
}
