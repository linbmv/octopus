package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/oauth"
)

const (
	codexQuotaEndpoint  = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaUserAgent = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"
	codexQuotaBodyLimit = 1 << 20
	codexQuotaCacheTTL  = 2 * time.Minute
	codexQuotaErrorTTL  = 15 * time.Second
)

// CodexQuotaWindow is one upstream rate-limit window. The upstream currently
// returns a primary seven-day window for Plus accounts and may add a secondary
// window in the future, so the fields intentionally mirror the wire contract.
type CodexQuotaWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAfterSeconds  int64 `json:"reset_after_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

type CodexQuotaRateLimit struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *CodexQuotaWindow `json:"primary_window,omitempty"`
	SecondaryWindow *CodexQuotaWindow `json:"secondary_window,omitempty"`
}

type CodexQuotaAdditionalRateLimit struct {
	LimitName      string               `json:"limit_name,omitempty"`
	MeteredFeature string               `json:"metered_feature,omitempty"`
	RateLimit      *CodexQuotaRateLimit `json:"rate_limit,omitempty"`
}

type CodexQuotaCredits struct {
	HasCredits          bool   `json:"has_credits"`
	Unlimited           bool   `json:"unlimited"`
	OverageLimitReached bool   `json:"overage_limit_reached"`
	Balance             string `json:"balance"`
}

// CodexQuota is deliberately limited to quota fields. The upstream response
// also contains account identifiers and email addresses, which must not be
// copied into an administrator API response unnecessarily.
type CodexQuota struct {
	ChannelKeyID         int                             `json:"channel_key_id"`
	KeyRemark            string                          `json:"key_remark,omitempty"`
	PlanType             string                          `json:"plan_type,omitempty"`
	RateLimit            *CodexQuotaRateLimit            `json:"rate_limit,omitempty"`
	CodeReviewRateLimit  *CodexQuotaRateLimit            `json:"code_review_rate_limit,omitempty"`
	AdditionalRateLimits []CodexQuotaAdditionalRateLimit `json:"additional_rate_limits,omitempty"`
	Credits              *CodexQuotaCredits              `json:"credits,omitempty"`
	FetchedAt            time.Time                       `json:"fetched_at"`
	Error                string                          `json:"error,omitempty"`
}

type codexQuotaResponse struct {
	PlanType             string                          `json:"plan_type"`
	RateLimit            *CodexQuotaRateLimit            `json:"rate_limit"`
	CodeReviewRateLimit  *CodexQuotaRateLimit            `json:"code_review_rate_limit"`
	AdditionalRateLimits []CodexQuotaAdditionalRateLimit `json:"additional_rate_limits"`
	Credits              *CodexQuotaCredits              `json:"credits"`
}

type codexQuotaCacheEntry struct {
	signature [32]byte
	value     CodexQuota
	expiresAt time.Time
}

var codexQuotaCache = struct {
	sync.Mutex
	entries map[int]codexQuotaCacheEntry
}{entries: make(map[int]codexQuotaCacheEntry)}

// QueryCodexQuota returns one quota result per enabled Codex OAuth key. A
// normal read uses a short in-memory cache; force bypasses it for the manual
// refresh action. Each key is isolated so one expired/broken credential does
// not hide the remaining keys' quota.
func QueryCodexQuota(ctx context.Context, channel *dbmodel.Channel, force bool) []CodexQuota {
	if channel == nil || channel.Type != dbmodel.ChannelTypeOpenAICodex {
		return nil
	}
	results := make([]CodexQuota, 0, len(channel.Keys))
	for _, key := range channel.Keys {
		if !key.Enabled || strings.TrimSpace(key.ChannelKey) == "" {
			continue
		}
		results = append(results, queryCodexQuotaForKey(ctx, channel, key, force))
	}
	return results
}

func queryCodexQuotaForKey(ctx context.Context, channel *dbmodel.Channel, key dbmodel.ChannelKey, force bool) CodexQuota {
	result := CodexQuota{ChannelKeyID: key.ID, KeyRemark: key.Remark}
	signature := codexProviderSignature(channel, key.ChannelKey)
	if !force {
		if cached, ok := getCodexQuota(key.ID, signature, time.Now()); ok {
			return cached
		}
	}

	provider, accountID, err := codexProviderForChannel(channel, key)
	if err == nil {
		var credentials *oauth.OAuthCredentials
		credentials, err = provider.Get(ctx)
		if err == nil {
			client, clientErr := helper.ChannelHttpClient(channel)
			if clientErr != nil {
				err = clientErr
			} else {
				result.PlanType, result.RateLimit, result.CodeReviewRateLimit, result.AdditionalRateLimits, result.Credits, err = fetchCodexQuota(ctx, client, credentials, accountID, codexQuotaEndpoint)
			}
		}
	}
	result.FetchedAt = time.Now().UTC()
	if err != nil {
		result.Error = publicCodexQuotaError(err)
	}
	putCodexQuota(key.ID, codexQuotaCacheEntry{signature: signature, value: result, expiresAt: time.Now().Add(codexQuotaTTL(result))})
	return result
}

func codexQuotaTTL(result CodexQuota) time.Duration {
	if result.Error != "" {
		return codexQuotaErrorTTL
	}
	return codexQuotaCacheTTL
}

func getCodexQuota(keyID int, signature [32]byte, now time.Time) (CodexQuota, bool) {
	if keyID <= 0 {
		return CodexQuota{}, false
	}
	codexQuotaCache.Lock()
	defer codexQuotaCache.Unlock()
	entry, ok := codexQuotaCache.entries[keyID]
	if !ok || entry.signature != signature || !entry.expiresAt.After(now) {
		if ok && !entry.expiresAt.After(now) {
			delete(codexQuotaCache.entries, keyID)
		}
		return CodexQuota{}, false
	}
	return entry.value, true
}

func putCodexQuota(keyID int, entry codexQuotaCacheEntry) {
	if keyID <= 0 {
		return
	}
	codexQuotaCache.Lock()
	codexQuotaCache.entries[keyID] = entry
	codexQuotaCache.Unlock()
}

func fetchCodexQuota(ctx context.Context, client *http.Client, credentials *oauth.OAuthCredentials, accountID, endpoint string) (string, *CodexQuotaRateLimit, *CodexQuotaRateLimit, []CodexQuotaAdditionalRateLimit, *CodexQuotaCredits, error) {
	if client == nil || credentials == nil || strings.TrimSpace(credentials.AccessToken) == "" {
		return "", nil, nil, nil, nil, errors.New("Codex OAuth credentials are unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, nil, nil, nil, errors.New("create Codex quota request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credentials.AccessToken))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", codexQuotaUserAgent)
	if strings.TrimSpace(accountID) != "" {
		request.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	}

	response, err := client.Do(request)
	if err != nil {
		return "", nil, nil, nil, nil, fmt.Errorf("send Codex quota request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, codexQuotaBodyLimit+1))
	if err != nil {
		return "", nil, nil, nil, nil, errors.New("read Codex quota response")
	}
	if len(body) > codexQuotaBodyLimit {
		return "", nil, nil, nil, nil, errors.New("Codex quota response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", nil, nil, nil, nil, fmt.Errorf("Codex quota request failed with HTTP status %d", response.StatusCode)
	}
	var decoded codexQuotaResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", nil, nil, nil, nil, errors.New("decode Codex quota response")
	}
	return strings.TrimSpace(decoded.PlanType), decoded.RateLimit, decoded.CodeReviewRateLimit, decoded.AdditionalRateLimits, decoded.Credits, nil
}

func publicCodexQuotaError(err error) string {
	if err == nil {
		return ""
	}
	// Keep provider/network internals out of the management response. HTTP
	// status is useful to an administrator and does not contain credentials.
	var statusErr interface{ Error() string }
	if errors.As(err, &statusErr) && strings.Contains(err.Error(), "HTTP status ") {
		return err.Error()
	}
	return "Codex quota refresh failed"
}
