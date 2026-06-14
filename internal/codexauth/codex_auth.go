package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

type CodexAuthData struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	Email        string
	PlanType     string
	ExpiresAt    int64 // unix seconds
}

// ParseCodexTokenJSON parses the FLAT CPA/Claude-Code style token json:
// {"id_token","access_token","refresh_token","account_id","last_refresh","email","expired","plan_type",...}
func ParseCodexTokenJSON(raw string) (*CodexAuthData, error) {
	var flat map[string]any
	if err := json.Unmarshal([]byte(raw), &flat); err != nil {
		return nil, err
	}

	data := &CodexAuthData{
		AccessToken:  getString(flat, "access_token"),
		RefreshToken: getString(flat, "refresh_token"),
		IDToken:      getString(flat, "id_token"),
		Email:        getString(flat, "email"),
		AccountID:    getString(flat, "account_id"),
		PlanType:     getString(flat, "plan_type"),
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required")
	}
	if data.RefreshToken == "" {
		return nil, fmt.Errorf("refresh_token is required")
	}

	if expired, ok := flat["expired"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, expired); err == nil {
			data.ExpiresAt = parsed.Unix()
		}
	}
	if data.ExpiresAt == 0 {
		data.ExpiresAt = time.Now().Add(time.Hour).Unix()
	}

	applyCodexIDTokenClaims(data, data.IDToken)
	return data, nil
}

func ApplyCodexOAuthImport(key *model.ChannelKey) error {
	if key == nil || key.CodexOAuthJSONInput == "" {
		return nil
	}
	data, err := ParseCodexTokenJSON(key.CodexOAuthJSONInput)
	if err != nil {
		return err
	}
	key.CodexAccessToken = data.AccessToken
	key.CodexRefreshToken = data.RefreshToken
	key.CodexIDToken = data.IDToken
	key.CodexTokenExpiry = data.ExpiresAt
	key.CodexAccountID = data.AccountID
	key.CodexPlanType = data.PlanType
	key.CodexEmail = data.Email
	if key.Remark == "" && data.Email != "" {
		key.Remark = data.Email
		if data.PlanType != "" {
			key.Remark += " (" + data.PlanType + ")"
		}
	}
	key.CodexOAuthJSONInput = ""
	return nil
}

// ToCredentials converts to axonhub oauth.OAuthCredentials.
func (d *CodexAuthData) ToCredentials() *oauth.OAuthCredentials {
	if d == nil {
		return nil
	}
	return &oauth.OAuthCredentials{
		ClientID:     codex.ClientID,
		AccessToken:  d.AccessToken,
		RefreshToken: d.RefreshToken,
		IDToken:      d.IDToken,
		ExpiresAt:    time.Unix(d.ExpiresAt, 0),
		TokenType:    "bearer",
		Scopes:       strings.Fields(codex.Scopes),
	}
}

// CodexCredentialsFromChannelKey builds oauth.OAuthCredentials from a stored ChannelKey's Codex* fields.
func CodexCredentialsFromChannelKey(key model.ChannelKey) *oauth.OAuthCredentials {
	return (&CodexAuthData{
		AccessToken:  key.CodexAccessToken,
		RefreshToken: key.CodexRefreshToken,
		IDToken:      key.CodexIDToken,
		ExpiresAt:    key.CodexTokenExpiry,
	}).ToCredentials()
}

// CodexAuthDataFromCredentials re-derives CodexAuthData from refreshed creds.
func CodexAuthDataFromCredentials(creds *oauth.OAuthCredentials) *CodexAuthData {
	if creds == nil {
		return nil
	}
	data := &CodexAuthData{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		IDToken:      creds.IDToken,
		ExpiresAt:    creds.ExpiresAt.Unix(),
	}
	applyCodexIDTokenClaims(data, data.IDToken)
	return data
}

func applyCodexIDTokenClaims(data *CodexAuthData, token string) {
	claims, err := parseJWTClaims(token)
	if err != nil {
		return
	}
	// Email fallback: top-level "email" → "https://api.openai.com/profile".email
	if email := getString(claims, "email"); email != "" {
		data.Email = email
	} else if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok {
		if email := getString(profile, "email"); email != "" {
			data.Email = email
		}
	}
	// Account/plan extraction with fallback (chatgpt_account_id → poid → user_id)
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID := getString(auth, "chatgpt_account_id"); accountID != "" {
			data.AccountID = accountID
		} else if poid := getString(auth, "poid"); poid != "" {
			data.AccountID = poid
		} else if userID := getString(auth, "user_id"); userID != "" {
			data.AccountID = userID
		}
		if planType := getString(auth, "chatgpt_plan_type"); planType != "" {
			data.PlanType = planType
		}
	}
}

func parseJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, err
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func getString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	str, _ := value.(string)
	return str
}
