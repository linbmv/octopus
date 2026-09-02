package relay

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestCircuitBreakerTripsAcrossRequestsAndKeepsKeysIndependent(t *testing.T) {
	now := time.Unix(100, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 2
	cfg.baseCooldown = 10 * time.Second
	cfg.maxCooldown = time.Minute
	keyA := circuitKey{channelID: 7, keyID: 11, model: "gpt-test"}
	keyB := circuitKey{channelID: 7, keyID: 12, model: "gpt-test"}

	if allowed, _, _ := breaker.allow(keyA, cfg); !allowed {
		t.Fatal("new candidate was unexpectedly blocked")
	}
	breaker.recordFailure(keyA, circuitPermit{}, cfg)
	if allowed, _, _ := breaker.allow(keyA, cfg); !allowed {
		t.Fatal("first failure should not trip the breaker")
	}
	breaker.recordFailure(keyA, circuitPermit{}, cfg)
	if allowed, _, remaining := breaker.allow(keyA, cfg); allowed || remaining != 10*time.Second {
		t.Fatalf("tripped candidate = (%t, %v), want blocked for 10s", allowed, remaining)
	}
	if allowed, _, _ := breaker.allow(keyB, cfg); !allowed {
		t.Fatal("a different credential must remain eligible")
	}

	now = now.Add(10 * time.Second)
	allowed, permit, _ := breaker.allow(keyA, cfg)
	if !allowed || permit.generation == 0 {
		t.Fatalf("half-open candidate = (%t, %+v), want a probe permit", allowed, permit)
	}
	breaker.recordSuccess(keyA, permit)
	if allowed, _, _ := breaker.allow(keyA, cfg); !allowed {
		t.Fatal("successful probe did not close the breaker")
	}
}

func TestCircuitBreakerBoundsHalfOpenProbesAndIgnoresLateProbe(t *testing.T) {
	now := time.Unix(200, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 1
	cfg.baseCooldown = time.Second
	cfg.maxCooldown = time.Minute
	cfg.maxProbes = 2
	cfg.probeLease = 5 * time.Second
	key := circuitKey{channelID: 1, keyID: 2, model: "model"}

	breaker.recordFailure(key, circuitPermit{}, cfg)
	now = now.Add(time.Second)
	allowed, first, _ := breaker.allow(key, cfg)
	if !allowed || first.generation == 0 {
		t.Fatal("first half-open probe was not admitted")
	}
	allowed, second, _ := breaker.allow(key, cfg)
	if !allowed || second.generation == first.generation {
		t.Fatal("second half-open probe was not admitted independently")
	}
	if allowed, _, remaining := breaker.allow(key, cfg); allowed || remaining != 5*time.Second {
		t.Fatalf("probe limit = (%t, %v), want blocked for 5s", allowed, remaining)
	}

	// The first probe closes the breaker. Its sibling completes late and must
	// not turn that successful state back into Open.
	breaker.recordSuccess(key, first)
	breaker.recordFailure(key, second, cfg)
	entry := breaker.entries[key]
	if entry.state != circuitClosed || entry.consecutiveFailures != 0 {
		t.Fatalf("late probe changed closed breaker: %+v", entry)
	}
}

func TestCircuitBreakerIgnoresLateNormalSuccessAfterTrip(t *testing.T) {
	now := time.Unix(250, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 1
	cfg.baseCooldown = time.Minute
	key := circuitKey{channelID: 5, keyID: 6, model: "model"}

	allowed, permit, _ := breaker.allow(key, cfg)
	if !allowed || permit.generation != 0 {
		t.Fatal("normal request was not admitted without a probe permit")
	}
	breaker.recordFailure(key, circuitPermit{}, cfg)
	breaker.recordSuccess(key, permit)
	if entry := breaker.entries[key]; entry.state != circuitOpen {
		t.Fatalf("late normal success closed the breaker: %+v", entry)
	}
}

func TestCircuitBreakerAbortReturnsHalfOpenProbeSlot(t *testing.T) {
	now := time.Unix(300, 0)
	breaker := newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 1
	cfg.baseCooldown = time.Second
	cfg.maxProbes = 1
	cfg.probeLease = time.Minute
	key := circuitKey{channelID: 3, keyID: 4, model: "model"}

	breaker.recordFailure(key, circuitPermit{}, cfg)
	now = now.Add(time.Second)
	_, permit, _ := breaker.allow(key, cfg)
	if allowed, _, _ := breaker.allow(key, cfg); allowed {
		t.Fatal("second probe passed before the first was settled")
	}
	breaker.abort(permit)
	if allowed, replacement, _ := breaker.allow(key, cfg); !allowed || replacement.generation == permit.generation {
		t.Fatal("aborted probe slot was not reusable")
	}
}

func TestSelectChannelEndpointForModelSkipsOpenCredential(t *testing.T) {
	previous := globalCircuitBreaker
	t.Cleanup(func() { globalCircuitBreaker = previous })
	now := time.Unix(400, 0)
	globalCircuitBreaker = newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 1
	key := circuitKey{channelID: 9, keyID: 1, model: "model"}
	globalCircuitBreaker.recordFailure(key, circuitPermit{}, cfg)

	channel := model.Channel{
		ID:      9,
		Name:    "multi-key",
		BaseURL: "https://upstream.example",
		Keys: []model.ChannelKey{
			{ID: 1, ChannelID: 9, Enabled: true, ChannelKey: "a"},
			{ID: 2, ChannelID: 9, Enabled: true, ChannelKey: "b"},
		},
	}
	selected, keyID, permit, err := selectChannelEndpointForModel(channel, 0, "model")
	if err != nil {
		t.Fatalf("select endpoint: %v", err)
	}
	if keyID != 2 || selected.Key != "b" || permit.generation != 0 {
		t.Fatalf("selected endpoint = key=%d value=%q permit=%+v, want key 2 without probe", keyID, selected.Key, permit)
	}
}

func TestCircuitSnapshotOmitsCredentialValuesAndResetClearsRuntimeState(t *testing.T) {
	previous := globalCircuitBreaker
	t.Cleanup(func() { globalCircuitBreaker = previous })
	now := time.Now()
	globalCircuitBreaker = newCircuitBreaker(func() time.Time { return now })
	cfg := defaultCircuitConfig()
	cfg.threshold = 1
	key := circuitKey{channelID: 77, keyID: 88, model: "gpt-test"}
	globalCircuitBreaker.recordFailure(key, circuitPermit{}, cfg)

	snapshot := CircuitSnapshotForChannel(77)
	if len(snapshot) != 1 || snapshot[0].State != "open" || snapshot[0].ChannelKeyID != 88 || snapshot[0].ModelName != "gpt-test" {
		t.Fatalf("circuit snapshot = %#v", snapshot)
	}
	for _, item := range snapshot {
		if item.ModelName == "secret-key" {
			t.Fatal("circuit snapshot exposed credential material")
		}
	}
	InvalidateChannelRuntimeState(77, "gpt-test")
	if got := CircuitSnapshotForChannel(77); len(got) != 0 {
		t.Fatalf("reset left circuit state: %#v", got)
	}
}
