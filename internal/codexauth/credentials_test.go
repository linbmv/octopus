package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

func TestParseFlatCredential(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	raw := `{"type":"codex","access_token":"` + testJWT(expiresAt) + `","refresh_token":"refresh","id_token":"id","account_id":"account","disabled":true,"expired":"` + expiresAt.Format(time.RFC3339) + `"}`
	document, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	credentials := document.Credentials()
	if credentials.AccessToken == "" || credentials.RefreshToken != "refresh" || credentials.IDToken != "id" {
		t.Fatalf("unexpected parsed credentials: access=%t refresh=%t id=%t", credentials.AccessToken != "", credentials.RefreshToken != "", credentials.IDToken != "")
	}
	if credentials.ClientID != codex.ClientID || !credentials.ExpiresAt.Equal(expiresAt) || document.AccountID() != "account" || !document.Disabled() {
		t.Fatalf("unexpected credential metadata: client=%q expires=%s account=%q disabled=%t", credentials.ClientID, credentials.ExpiresAt, document.AccountID(), document.Disabled())
	}
}

func TestParseNestedCredentialAndJWTExpiration(t *testing.T) {
	expiresAt := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)
	raw := `{"tokens":{"access_token":"` + testJWT(expiresAt) + `","refresh_token":"refresh"}}`
	document, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !document.Credentials().ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %s, want %s", document.Credentials().ExpiresAt, expiresAt)
	}
}

func TestParseCredentialWithUnknownExpirationRequiresImmediateRefresh(t *testing.T) {
	document, err := Parse(`{"type":"codex","access_token":"opaque","refresh_token":"refresh"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !document.Credentials().ExpiresAt.IsZero() {
		t.Fatalf("expires_at = %s, want zero so the token is refreshed immediately", document.Credentials().ExpiresAt)
	}
}

func TestWithRefreshedPreservesFlatMetadata(t *testing.T) {
	document, err := Parse(`{"type":"codex","access_token":"old","refresh_token":"old-refresh","account_id":"account","email":"person@example.test","disabled":true,"vendor":{"keep":true}}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	updated, err := document.WithRefreshed(&oauth.OAuthCredentials{AccessToken: "new", RefreshToken: "new-refresh", IDToken: "new-id", ExpiresAt: expiresAt}, now)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(updated), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["access_token"] != "new" || decoded["refresh_token"] != "new-refresh" || decoded["account_id"] != "account" || decoded["email"] != "person@example.test" || decoded["disabled"] != true {
		t.Fatalf("refreshed flat document lost fields: %#v", decoded)
	}
	if vendor, ok := decoded["vendor"].(map[string]any); !ok || vendor["keep"] != true {
		t.Fatalf("unknown metadata was not preserved: %#v", decoded["vendor"])
	}
}

func TestWithRefreshedPreservesNestedShape(t *testing.T) {
	document, err := Parse(`{"last_refresh":"2026-08-02T00:00:00Z","tokens":{"access_token":"old","refresh_token":"refresh","provider":"keep"},"other":7}`)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := document.WithRefreshed(&oauth.OAuthCredentials{AccessToken: "new", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Tokens map[string]any `json:"tokens"`
		Other  int            `json:"other"`
	}
	if err := json.Unmarshal([]byte(updated), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Tokens["access_token"] != "new" || decoded.Tokens["provider"] != "keep" || decoded.Other != 7 {
		t.Fatalf("refreshed nested document lost fields: %#v", decoded)
	}
}

func TestParseRejectsNonCredentialWithoutLeakingInput(t *testing.T) {
	secret := "must-not-appear"
	_, err := Parse(`{"type":"other","access_token":"` + secret + `"}`)
	if err == nil {
		t.Fatal("expected invalid credential error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked credential input: %v", err)
	}
}

func testJWT(expiresAt time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"exp": expiresAt.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
