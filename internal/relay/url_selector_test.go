package relay

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRuntimeURLSelectorRecordsLatencyAndPrefersFasterKnownURL(t *testing.T) {
	selector := newRuntimeURLSelector()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }
	urls := []model.BaseUrl{{URL: "https://slow.example"}, {URL: "https://fast.example"}}

	selector.RecordSuccess(1, "https://slow.example", 900*time.Millisecond)
	selector.RecordSuccess(1, "https://fast.example", 100*time.Millisecond)

	for range 20 {
		if got := selector.Select(1, urls); got != "https://fast.example" {
			t.Fatalf("Select() = %q, want fast URL", got)
		}
	}
}

func TestRuntimeURLSelectorSkipsCooledURL(t *testing.T) {
	selector := newRuntimeURLSelector()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }
	urls := []model.BaseUrl{{URL: "https://a.example"}, {URL: "https://b.example"}}

	selector.RecordSuccess(1, "https://a.example", 50*time.Millisecond)
	selector.RecordSuccess(1, "https://b.example", 500*time.Millisecond)
	selector.RecordFailure(1, "https://a.example")

	if got := selector.Select(1, urls); got != "https://b.example" {
		t.Fatalf("Select() = %q, want non-cooled URL", got)
	}
}

func TestRuntimeURLSelectorRestoresURLAfterCooldown(t *testing.T) {
	selector := newRuntimeURLSelector()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }
	urls := []model.BaseUrl{{URL: "https://a.example"}, {URL: "https://b.example"}}

	selector.RecordSuccess(1, "https://a.example", 50*time.Millisecond)
	selector.RecordSuccess(1, "https://b.example", 500*time.Millisecond)
	selector.RecordFailure(1, "https://a.example")
	now = now.Add(selector.cooldownBase + time.Second)

	for range 20 {
		if got := selector.Select(1, urls); got != "https://a.example" {
			t.Fatalf("Select() = %q, want restored faster URL", got)
		}
	}
}

func TestRuntimeURLSelectorDeduplicatesAndIgnoresEmptyURLs(t *testing.T) {
	selector := newRuntimeURLSelector()
	urls := []model.BaseUrl{{URL: ""}, {URL: "https://a.example"}, {URL: "https://a.example"}}

	if got := selector.Select(1, urls); got != "https://a.example" {
		t.Fatalf("Select() = %q, want only valid URL", got)
	}
}
