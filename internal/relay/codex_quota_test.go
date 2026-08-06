package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/oauth"
)

func TestFetchCodexQuotaParsesWindowsAndKeepsHeadersScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "account-1" {
			t.Fatalf("account id = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != codexQuotaUserAgent {
			t.Fatalf("user agent = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":93,"limit_window_seconds":604800,"reset_after_seconds":60,"reset_at":1786175391},"secondary_window":null},"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":"0"}}`))
	}))
	defer server.Close()

	plan, limit, _, _, credits, err := fetchCodexQuota(context.Background(), server.Client(), &oauth.OAuthCredentials{AccessToken: "access-token", ExpiresAt: time.Now().Add(time.Hour)}, "account-1", server.URL)
	if err != nil {
		t.Fatalf("fetchCodexQuota() error = %v", err)
	}
	if plan != "plus" || limit == nil || limit.PrimaryWindow == nil || limit.PrimaryWindow.UsedPercent != 93 || limit.PrimaryWindow.ResetAt != 1786175391 {
		t.Fatalf("quota limit = %#v, plan = %q", limit, plan)
	}
	if credits == nil || credits.Balance != "0" || credits.Unlimited {
		t.Fatalf("credits = %#v", credits)
	}
}

func TestFetchCodexQuotaDoesNotExposeUpstreamBodyOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"access_token":"must-not-leak"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, _, _, _, _, err := fetchCodexQuota(context.Background(), server.Client(), &oauth.OAuthCredentials{AccessToken: "access-token"}, "", server.URL)
	if err == nil || err.Error() != "codex quota request failed with HTTP status 401" {
		t.Fatalf("error = %v", err)
	}
}

func TestCredentialAccountHintIsStableAndNonReversible(t *testing.T) {
	first := credentialAccountHint("account-one", 7)
	if first == "account-one" || !strings.HasPrefix(first, "acct-") {
		t.Fatalf("account hint = %q, want an acct- fingerprint", first)
	}
	if again := credentialAccountHint("account-one", 99); again != first {
		t.Fatalf("same account produced different hints: %q and %q", first, again)
	}
	if other := credentialAccountHint("account-two", 7); other == first {
		t.Fatalf("different accounts produced the same hint: %q", first)
	}
	if fallback := credentialAccountHint("", 7); fallback != "key-7" {
		t.Fatalf("empty account hint = %q, want key-7", fallback)
	}
}

func TestQueryCodexQuotaForKeySelectsOnlyRequestedEnabledKey(t *testing.T) {
	channel := &dbmodel.Channel{
		Type: dbmodel.ChannelTypeOpenAICodex,
		Keys: []dbmodel.ChannelKey{
			{ID: 11, Enabled: true, ChannelKey: `{"type":"codex","access_token":"first"}`},
			{ID: 12, Enabled: true, ChannelKey: `{"type":"codex","access_token":"second"}`},
			{ID: 13, Enabled: false, ChannelKey: `{"type":"codex","access_token":"disabled"}`},
		},
	}
	quotas := QueryCodexQuotaForKey(context.Background(), channel, 12, true)
	if len(quotas) != 1 || quotas[0].ChannelKeyID != 12 {
		t.Fatalf("selected quotas = %#v, want only key 12", quotas)
	}
	if disabled := QueryCodexQuotaForKey(context.Background(), channel, 13, true); disabled != nil {
		t.Fatalf("disabled key returned quotas: %#v", disabled)
	}
}
