package relay

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/looplj/axonhub/llm"
)

func TestRelayRunUpstreamAttemptBudgetIsExact(t *testing.T) {
	run := &relayRun{maxUpstreamAttempts: 2}
	if !run.reserveUpstreamAttempt() || !run.reserveUpstreamAttempt() {
		t.Fatal("first two upstream attempts should be allowed")
	}
	if run.reserveUpstreamAttempt() {
		t.Fatal("third upstream attempt should be rejected")
	}
	if run.upstreamAttempts != 2 {
		t.Fatalf("upstream attempt count = %d, want 2", run.upstreamAttempts)
	}
	unlimited := &relayRun{}
	for range 100 {
		if !unlimited.reserveUpstreamAttempt() {
			t.Fatal("zero max attempts should disable the limit")
		}
	}
}

func TestRelayRunStreamFirstEventBudgetTracksRemainingTime(t *testing.T) {
	run := &relayRun{
		streamFirstEventBudget: 90 * time.Second,
		streamFirstEventSpent:  35 * time.Second,
	}
	if got := run.remainingStreamFirstEventBudget(); got != 55*time.Second {
		t.Fatalf("remaining budget = %v, want 55s", got)
	}
	run.streamFirstEventSpent = 100 * time.Second
	if got := run.remainingStreamFirstEventBudget(); got != 0 {
		t.Fatalf("overdrawn remaining budget = %v, want 0", got)
	}
	if got := (&relayRun{}).remainingStreamFirstEventBudget(); got != 0 {
		t.Fatalf("disabled remaining budget = %v, want 0", got)
	}
}

func TestFirstTokenBudgetTimeoutIsDistinctFromAdaptiveTimeout(t *testing.T) {
	budgetErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutBudget}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if !isFirstTokenBudgetTimeout(budgetErr) {
		t.Fatal("budget timeout was not recognized")
	}
	adaptiveErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutAdaptive}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if isFirstTokenBudgetTimeout(adaptiveErr) {
		t.Fatal("adaptive timeout was classified as request budget exhaustion")
	}
}

func TestStreamFirstEventBudgetCountsSuccessfulFirstTokenWait(t *testing.T) {
	stream := true
	run := &relayRun{
		internalRequest:        &llm.Request{Stream: &stream},
		streamFirstEventBudget: time.Second,
	}
	iter := balancer.NewIterator(model.Group{Items: []model.GroupItem{{ChannelID: 1, ModelName: "m"}}}, 0, "m")
	if !iter.Next() {
		t.Fatal("iterator has no candidate")
	}
	span := iter.StartAttempt(1, 1, "channel", "key")
	span.RecordFirstToken(time.Now().Add(50 * time.Millisecond))
	run.recordStreamFirstEventWait(span, nil)
	if run.streamFirstEventSpent < 40*time.Millisecond || run.streamFirstEventSpent > 200*time.Millisecond {
		t.Fatalf("successful first token wait = %v, want approximately 50ms", run.streamFirstEventSpent)
	}
}
