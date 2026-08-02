package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
