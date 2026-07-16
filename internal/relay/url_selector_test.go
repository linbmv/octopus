package relay

import (
	"fmt"
	"sync"
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

func TestRuntimeURLSelectorEnforcesStateLimits(t *testing.T) {
	selector := newRuntimeURLSelector()
	selector.maxEntries = 3
	selector.sweepEvery = 0
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }

	for i := range 12 {
		now = now.Add(time.Second)
		selector.RecordSuccess(1, fmt.Sprintf("https://latency-%d.example", i), time.Second)
		selector.RecordFailure(1, fmt.Sprintf("https://cooldown-%d.example", i))
		if len(selector.latencies) > selector.maxEntries {
			t.Fatalf("latency state size = %d, limit = %d", len(selector.latencies), selector.maxEntries)
		}
		if len(selector.cooldowns) > selector.maxEntries {
			t.Fatalf("cooldown state size = %d, limit = %d", len(selector.cooldowns), selector.maxEntries)
		}
	}

	oldLatency := runtimeURLKey{ChannelID: 1, URL: "https://latency-0.example"}
	if _, ok := selector.latencies[oldLatency]; ok {
		t.Fatal("oldest latency entry was not evicted")
	}
	oldCooldown := runtimeURLKey{ChannelID: 1, URL: "https://cooldown-0.example"}
	if _, ok := selector.cooldowns[oldCooldown]; ok {
		t.Fatal("oldest cooldown entry was not evicted")
	}
}

func TestRuntimeURLSelectorSweepsExpiredState(t *testing.T) {
	selector := newRuntimeURLSelector()
	selector.stateTTL = time.Hour
	selector.sweepEvery = 1
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return now }

	latencyKey := runtimeURLKey{ChannelID: 1, URL: "https://latency.example"}
	cooldownKey := runtimeURLKey{ChannelID: 1, URL: "https://cooldown.example"}
	selector.RecordSuccess(latencyKey.ChannelID, latencyKey.URL, time.Second)
	selector.RecordFailure(cooldownKey.ChannelID, cooldownKey.URL)

	now = now.Add(selector.stateTTL + time.Second)
	selector.RecordSuccess(2, "https://fresh.example", time.Second)

	if _, ok := selector.latencies[latencyKey]; ok {
		t.Fatal("expired latency entry was not swept")
	}
	if _, ok := selector.cooldowns[cooldownKey]; ok {
		t.Fatal("expired cooldown entry was not swept")
	}
}

func TestRuntimeURLSelectorInvalidateChannel(t *testing.T) {
	selector := newRuntimeURLSelector()
	selector.RecordSuccess(1, "https://one-latency.example", time.Second)
	selector.RecordFailure(1, "https://one-cooldown.example")
	selector.RecordSuccess(2, "https://two-latency.example", time.Second)
	selector.RecordFailure(2, "https://two-cooldown.example")

	selector.InvalidateChannel(1)

	for key := range selector.latencies {
		if key.ChannelID == 1 {
			t.Fatalf("channel 1 latency state remains for %q", key.URL)
		}
	}
	for key := range selector.cooldowns {
		if key.ChannelID == 1 {
			t.Fatalf("channel 1 cooldown state remains for %q", key.URL)
		}
	}
	if len(selector.latencies) != 1 || len(selector.cooldowns) != 1 {
		t.Fatalf("other channel state was removed: latencies=%d cooldowns=%d", len(selector.latencies), len(selector.cooldowns))
	}
}

func TestRuntimeURLSelectorConcurrentAccessAndInvalidation(t *testing.T) {
	selector := newRuntimeURLSelector()
	selector.maxEntries = 32
	urls := []model.BaseUrl{{URL: "https://a.example"}, {URL: "https://b.example"}}

	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				channelID := (worker+i)%8 + 1
				baseURL := fmt.Sprintf("https://runtime-%d.example", (worker+i)%64)
				selector.RecordSuccess(channelID, baseURL, time.Duration(i+1)*time.Millisecond)
				selector.RecordFailure(channelID, baseURL)
				_ = selector.Select(channelID, urls)
				if i%29 == 0 {
					selector.InvalidateChannel(channelID)
				}
			}
		}()
	}
	wg.Wait()

	selector.mu.RLock()
	defer selector.mu.RUnlock()
	if len(selector.latencies) > selector.maxEntries {
		t.Fatalf("latency state size = %d, limit = %d", len(selector.latencies), selector.maxEntries)
	}
	if len(selector.cooldowns) > selector.maxEntries {
		t.Fatalf("cooldown state size = %d, limit = %d", len(selector.cooldowns), selector.maxEntries)
	}
}
