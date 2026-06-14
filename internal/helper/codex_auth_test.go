package helper

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestParseCodexTokenJSON(t *testing.T) {
	idToken := makeTestJWT(t, map[string]any{
		"email": "u@x.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc123",
			"chatgpt_plan_type":  "team",
		},
	})
	raw := `{
		"id_token": ` + quoteJSON(t, idToken) + `,
		"access_token": "access-token",
		"refresh_token": "refresh-token",
		"account_id": "top-level-account",
		"last_refresh": "2026-06-10T00:00:00Z",
		"email": "top@x.com",
		"expired": "2025-01-02T03:04:05Z",
		"plan_type": "free"
	}`

	data, err := ParseCodexTokenJSON(raw)
	if err != nil {
		t.Fatalf("ParseCodexTokenJSON() error = %v", err)
	}
	if data.AccessToken != "access-token" {
		t.Fatalf("AccessToken = %q, want access-token", data.AccessToken)
	}
	if data.RefreshToken != "refresh-token" {
		t.Fatalf("RefreshToken = %q, want refresh-token", data.RefreshToken)
	}
	if data.AccountID != "acc123" {
		t.Fatalf("AccountID = %q, want acc123", data.AccountID)
	}
	if data.Email != "u@x.com" {
		t.Fatalf("Email = %q, want u@x.com", data.Email)
	}
	if data.PlanType != "team" {
		t.Fatalf("PlanType = %q, want team", data.PlanType)
	}
	wantExpiry := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Unix()
	if data.ExpiresAt != wantExpiry {
		t.Fatalf("ExpiresAt = %d, want %d", data.ExpiresAt, wantExpiry)
	}
}

func TestParseCodexTokenJSONMissingAccessToken(t *testing.T) {
	_, err := ParseCodexTokenJSON(`{"refresh_token":"refresh-token"}`)
	if err == nil {
		t.Fatal("ParseCodexTokenJSON() error = nil, want error")
	}
}

func TestCodexCredentialsRoundTrip(t *testing.T) {
	idToken := makeTestJWT(t, map[string]any{
		"email": "u@x.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc123",
			"chatgpt_plan_type":  "team",
		},
	})
	data := &CodexAuthData{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		IDToken:      idToken,
		ExpiresAt:    time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Unix(),
	}

	creds := data.ToCredentials()
	if creds == nil {
		t.Fatal("ToCredentials() = nil")
	}
	if creds.AccessToken != data.AccessToken || creds.RefreshToken != data.RefreshToken || creds.IDToken != data.IDToken {
		t.Fatalf("credentials token fields did not round-trip")
	}
	if creds.TokenType != "bearer" {
		t.Fatalf("TokenType = %q, want bearer", creds.TokenType)
	}
	if len(creds.Scopes) != 4 {
		t.Fatalf("Scopes length = %d, want 4", len(creds.Scopes))
	}
	if got := creds.ExpiresAt.Unix(); got != data.ExpiresAt {
		t.Fatalf("ExpiresAt = %d, want %d", got, data.ExpiresAt)
	}

	got := CodexAuthDataFromCredentials(creds)
	if got.AccessToken != data.AccessToken || got.RefreshToken != data.RefreshToken || got.IDToken != data.IDToken {
		t.Fatalf("CodexAuthDataFromCredentials token fields did not round-trip")
	}
	if got.AccountID != "acc123" || got.Email != "u@x.com" || got.PlanType != "team" {
		t.Fatalf("CodexAuthDataFromCredentials claims = (%q, %q, %q), want (acc123, u@x.com, team)", got.AccountID, got.Email, got.PlanType)
	}
}

func TestApplyCodexOAuthImport(t *testing.T) {
	idToken := makeTestJWT(t, map[string]any{
		"email": "u@x.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc123",
			"chatgpt_plan_type":  "team",
		},
	})
	key := &model.ChannelKey{
		CodexOAuthJSONInput: `{
			"id_token": ` + quoteJSON(t, idToken) + `,
			"access_token": "access-token",
			"refresh_token": "refresh-token",
			"expired": "2025-01-02T03:04:05Z"
		}`,
	}

	if err := ApplyCodexOAuthImport(key); err != nil {
		t.Fatalf("ApplyCodexOAuthImport() error = %v", err)
	}
	if key.CodexAccessToken != "access-token" || key.CodexRefreshToken != "refresh-token" || key.CodexIDToken != idToken {
		t.Fatalf("token fields not populated: %#v", key)
	}
	if key.CodexAccountID != "acc123" || key.CodexEmail != "u@x.com" || key.CodexPlanType != "team" {
		t.Fatalf("claim fields = (%q, %q, %q), want (acc123, u@x.com, team)", key.CodexAccountID, key.CodexEmail, key.CodexPlanType)
	}
	if key.CodexTokenExpiry != time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Unix() {
		t.Fatalf("CodexTokenExpiry = %d", key.CodexTokenExpiry)
	}
	if key.Remark != "u@x.com (team)" {
		t.Fatalf("Remark = %q, want u@x.com (team)", key.Remark)
	}
	if key.CodexOAuthJSONInput != "" {
		t.Fatalf("CodexOAuthJSONInput was not cleared")
	}
}

func makeTestJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payloadJSON) + ".signature"
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
