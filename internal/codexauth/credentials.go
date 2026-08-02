package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

const OfficialBaseURL = "https://chatgpt.com/backend-api/codex"

// Document retains the original credential document so token refreshes can be
// written back without discarding account metadata owned by the importing
// tool. Both the flat CLIProxyAPI/MeowCLI shape and Codex's nested auth.json
// shape are accepted.
type Document struct {
	root        map[string]json.RawMessage
	nested      bool
	credentials oauth.OAuthCredentials
	accountID   string
	disabled    bool
}

type flatCredential struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
	Expired      string `json:"expired"`
	ExpiresAt    string `json:"expires_at"`
	LastRefresh  string `json:"last_refresh"`
	Disabled     bool   `json:"disabled"`
}

type nestedCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

// Parse validates a stored Codex OAuth JSON document without ever including
// credential values in returned errors.
func Parse(raw string) (*Document, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("codex OAuth JSON is empty")
	}

	root := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil || root == nil {
		return nil, errors.New("codex OAuth credential must be a JSON object")
	}

	var flat flatCredential
	if err := json.Unmarshal([]byte(trimmed), &flat); err != nil {
		return nil, errors.New("codex OAuth credential has invalid fields")
	}
	if kind := strings.TrimSpace(flat.Type); kind != "" && !strings.EqualFold(kind, "codex") {
		return nil, fmt.Errorf("codex OAuth credential type must be codex")
	}

	accessToken := strings.TrimSpace(flat.AccessToken)
	refreshToken := strings.TrimSpace(flat.RefreshToken)
	idToken := strings.TrimSpace(flat.IDToken)
	nested := false
	if rawTokens, ok := root["tokens"]; ok {
		var tokens nestedCredential
		if err := json.Unmarshal(rawTokens, &tokens); err != nil {
			return nil, errors.New("codex OAuth tokens must be a JSON object")
		}
		nested = true
		if accessToken == "" {
			accessToken = strings.TrimSpace(tokens.AccessToken)
		}
		if refreshToken == "" {
			refreshToken = strings.TrimSpace(tokens.RefreshToken)
		}
		if idToken == "" {
			idToken = strings.TrimSpace(tokens.IDToken)
		}
	}
	if accessToken == "" {
		return nil, errors.New("codex OAuth credential access_token is required")
	}

	expiresAt := firstCredentialTime(flat.Expired, flat.ExpiresAt)
	if expiresAt.IsZero() {
		expiresAt = jwtExpiration(accessToken)
	}
	if expiresAt.IsZero() && flat.LastRefresh != "" {
		if refreshedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(flat.LastRefresh)); err == nil {
			expiresAt = refreshedAt.Add(time.Hour)
		}
	}
	return &Document{
		root:      root,
		nested:    nested,
		accountID: strings.TrimSpace(flat.AccountID),
		disabled:  flat.Disabled,
		credentials: oauth.OAuthCredentials{
			ClientID:     codex.ClientID,
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			IDToken:      idToken,
			ExpiresAt:    expiresAt,
			TokenType:    "bearer",
			Scopes:       strings.Fields(codex.Scopes),
		},
	}, nil
}

func (d *Document) Credentials() *oauth.OAuthCredentials {
	if d == nil {
		return nil
	}
	credentials := d.credentials
	credentials.Scopes = append([]string(nil), d.credentials.Scopes...)
	return &credentials
}

func (d *Document) AccountID() string {
	if d == nil {
		return ""
	}
	return d.accountID
}

// Disabled is preserved as import metadata. Octopus's ChannelKey.Enabled flag
// remains authoritative, because external tools use disabled for local routing
// state rather than for OAuth token validity.
func (d *Document) Disabled() bool {
	return d != nil && d.disabled
}

// WithRefreshed returns the original JSON shape with only OAuth token and
// timestamp fields updated. Unknown metadata is retained byte-for-byte at the
// JSON value level.
func (d *Document) WithRefreshed(refreshed *oauth.OAuthCredentials, now time.Time) (string, error) {
	if d == nil || refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
		return "", errors.New("refreshed Codex OAuth credentials are incomplete")
	}
	root := cloneRawObject(d.root)
	if root == nil {
		return "", errors.New("codex OAuth credential document is unavailable")
	}

	if d.nested {
		tokens := make(map[string]json.RawMessage)
		if rawTokens, ok := root["tokens"]; ok {
			if err := json.Unmarshal(rawTokens, &tokens); err != nil {
				return "", errors.New("codex OAuth tokens must be a JSON object")
			}
		}
		setRawString(tokens, "access_token", refreshed.AccessToken)
		setRawString(tokens, "refresh_token", firstNonEmpty(refreshed.RefreshToken, d.credentials.RefreshToken))
		setRawStringIfPresent(tokens, "id_token", firstNonEmpty(refreshed.IDToken, d.credentials.IDToken))
		encodedTokens, err := json.Marshal(tokens)
		if err != nil {
			return "", errors.New("encode refreshed Codex OAuth tokens")
		}
		root["tokens"] = encodedTokens
	} else {
		setRawString(root, "access_token", refreshed.AccessToken)
		setRawString(root, "refresh_token", firstNonEmpty(refreshed.RefreshToken, d.credentials.RefreshToken))
		setRawStringIfPresent(root, "id_token", firstNonEmpty(refreshed.IDToken, d.credentials.IDToken))
	}

	if now.IsZero() {
		now = time.Now()
	}
	setRawString(root, "last_refresh", now.UTC().Format(time.RFC3339Nano))
	if !refreshed.ExpiresAt.IsZero() {
		expiration := refreshed.ExpiresAt.UTC().Format(time.RFC3339Nano)
		if _, ok := root["expires_at"]; ok {
			setRawString(root, "expires_at", expiration)
		} else {
			setRawString(root, "expired", expiration)
		}
	}

	encoded, err := json.Marshal(root)
	if err != nil {
		return "", errors.New("encode refreshed Codex OAuth credential")
	}
	return string(encoded), nil
}

func cloneRawObject(source map[string]json.RawMessage) map[string]json.RawMessage {
	if source == nil {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}

func setRawString(target map[string]json.RawMessage, key, value string) {
	encoded, _ := json.Marshal(value)
	target[key] = encoded
}

func setRawStringIfPresent(target map[string]json.RawMessage, key, value string) {
	if value == "" {
		return
	}
	if _, ok := target[key]; ok {
		setRawString(target, key, value)
	}
}

func firstCredentialTime(values ...string) time.Time {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
		if unix, err := strconv.ParseInt(value, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0)
		}
	}
	return time.Time{}
}

func jwtExpiration(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil || claims.ExpiresAt == "" {
		return time.Time{}
	}
	unix, err := claims.ExpiresAt.Int64()
	if err != nil || unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
