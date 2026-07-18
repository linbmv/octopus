package relay

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterDurationUsesProviderDeadline(t *testing.T) {
	now := time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC)
	if got := retryAfterDuration(http.Header{"Retry-After": {"7"}}, now, time.Minute); got != 7*time.Second {
		t.Fatalf("numeric Retry-After = %v, want 7s", got)
	}
	date := now.Add(90 * time.Second).Format(http.TimeFormat)
	if got := retryAfterDuration(http.Header{"Retry-After": {date}}, now, time.Minute); got != 90*time.Second {
		t.Fatalf("date Retry-After = %v, want 90s", got)
	}
	if got := retryAfterDuration(nil, now, time.Minute); got != time.Minute {
		t.Fatalf("missing Retry-After = %v, want fallback 1m", got)
	}
}

func TestChannelRateLimitStateExpiresAndInvalidates(t *testing.T) {
	now := time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC)
	state := newChannelRateLimitState()
	state.now = func() time.Time { return now }
	state.record(7, 30*time.Second)
	if got := state.remaining(7); got != 30*time.Second {
		t.Fatalf("remaining = %v, want 30s", got)
	}
	state.invalidate(7)
	if got := state.remaining(7); got != 0 {
		t.Fatalf("remaining after invalidation = %v", got)
	}
	state.record(7, 30*time.Second)
	now = now.Add(31 * time.Second)
	if got := state.remaining(7); got != 0 {
		t.Fatalf("remaining after expiry = %v", got)
	}
}
