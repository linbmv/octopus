package balancer

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func resetBalancerRuntimeState() {
	globalSession.clear()
	globalBreaker.clear()
	smoothWeightedState.clear()
	smoothRoundRobinState.clear()
	SetHealthWeightFunc(nil)
}

func TestStickyNormalizesRequestModel(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	SetSticky(7, "  GPT-4O  ", 11, 101, "upstream-model")
	entry := GetSticky(7, "gpt-4o", time.Minute)
	if entry == nil {
		t.Fatal("GetSticky() = nil, want normalized model hit")
	}
	if entry.ChannelID != 11 || entry.ChannelKeyID != 101 {
		t.Fatalf("sticky entry = (%d, %d), want (11, 101)", entry.ChannelID, entry.ChannelKeyID)
	}

	// The returned value must not let callers mutate shared cache state.
	entry.ChannelID = 999
	again := GetSticky(7, " GPT-4O ", time.Minute)
	if again == nil || again.ChannelID != 11 {
		t.Fatalf("cached entry was mutated through returned pointer: %#v", again)
	}
}

func TestStickyStateHardLimitAndExpirySweep(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	originalNow := sessionNow
	defer func() { sessionNow = originalNow }()
	base := time.Unix(1_700_000_000, 0)
	sessionNow = func() time.Time { return base }

	for i := 0; i < stickyStateLimit+32; i++ {
		SetSticky(i, fmt.Sprintf("model-%d", i), i, i, "actual")
	}
	if got := globalSession.len(); got != stickyStateLimit {
		t.Fatalf("sticky state length = %d, want hard limit %d", got, stickyStateLimit)
	}
	if got := GetSticky(stickyStateLimit+31, fmt.Sprintf("model-%d", stickyStateLimit+31), time.Hour); got == nil {
		t.Fatal("most recently inserted sticky entry was evicted")
	}

	base = base.Add(stickyStateMaxAge + time.Second)
	globalSession.mu.Lock()
	globalSession.sweepExpiredLocked(base)
	globalSession.mu.Unlock()
	if got := globalSession.len(); got != 0 {
		t.Fatalf("sticky state length after TTL sweep = %d, want 0", got)
	}
}

func TestCircuitStateHardLimitExpiryAndChannelInvalidation(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < circuitStateLimit+32; i++ {
		getOrCreateEntry(circuitKey(i, i, fmt.Sprintf("model-%d", i)), base)
	}
	if got := globalBreaker.len(); got != circuitStateLimit {
		t.Fatalf("circuit state length = %d, want hard limit %d", got, circuitStateLimit)
	}

	globalBreaker.clear()
	getOrCreateEntry(circuitKey(10, 100, "a"), base)
	getOrCreateEntry(circuitKey(10, 101, "b"), base)
	getOrCreateEntry(circuitKey(20, 200, "c"), base)
	InvalidateChannel(10)
	if got := globalBreaker.len(); got != 1 {
		t.Fatalf("circuit state length after channel invalidation = %d, want 1", got)
	}
	if globalBreaker.get(circuitKey(20, 200, "c")) == nil {
		t.Fatal("channel invalidation removed unrelated circuit state")
	}

	globalBreaker.sweepExpired(base.Add(circuitStateTTL + time.Second))
	if got := globalBreaker.len(); got != 0 {
		t.Fatalf("circuit state length after TTL sweep = %d, want 0", got)
	}
}

func TestWeightedStateHardLimitExpiryAndInvalidation(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	originalNow := weightedNow
	defer func() { weightedNow = originalNow }()
	base := time.Unix(1_700_000_000, 0)
	weightedNow = func() time.Time { return base }
	weighted := &Weighted{}

	for i := 0; i < weightedStateLimit+32; i++ {
		weighted.Candidates([]model.GroupItem{
			{ID: i*2 + 1, ChannelID: i*2 + 1, ModelName: fmt.Sprintf("a-%d", i), Weight: 1},
			{ID: i*2 + 2, ChannelID: i*2 + 2, ModelName: fmt.Sprintf("b-%d", i), Weight: 1},
		})
	}
	if got := smoothWeightedState.len(); got != weightedStateLimit {
		t.Fatalf("weighted state length = %d, want hard limit %d", got, weightedStateLimit)
	}

	smoothWeightedState.clear()
	itemsA := []model.GroupItem{
		{ID: 1, ChannelID: 10, ModelName: "a", Weight: 1},
		{ID: 2, ChannelID: 11, ModelName: "b", Weight: 1},
	}
	itemsB := []model.GroupItem{
		{ID: 3, ChannelID: 20, ModelName: "c", Weight: 1},
		{ID: 4, ChannelID: 21, ModelName: "d", Weight: 1},
	}
	weighted.Candidates(itemsA)
	weighted.Candidates(itemsB)
	InvalidateChannel(10)
	if got := smoothWeightedState.len(); got != 1 {
		t.Fatalf("weighted state length after channel invalidation = %d, want 1", got)
	}

	base = base.Add(weightedStateTTL + time.Second)
	smoothWeightedState.mu.Lock()
	smoothWeightedState.sweepExpiredLocked(base)
	smoothWeightedState.mu.Unlock()
	if got := smoothWeightedState.len(); got != 0 {
		t.Fatalf("weighted state length after TTL sweep = %d, want 0", got)
	}
}

func TestRoundRobinStateHardLimitAndInvalidation(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()
	originalNow := roundRobinNow
	defer func() { roundRobinNow = originalNow }()
	base := time.Unix(1_700_000_000, 0)
	roundRobinNow = func() time.Time { return base }
	for i := 0; i < roundRobinStateLimit+32; i++ {
		(&RoundRobin{}).Candidates([]model.GroupItem{{ID: i*2 + 1, ChannelID: i + 1}, {ID: i*2 + 2, ChannelID: i + 1, ModelName: "b"}})
	}
	if got := smoothRoundRobinState.len(); got != roundRobinStateLimit {
		t.Fatalf("round robin state length = %d, want %d", got, roundRobinStateLimit)
	}
	smoothRoundRobinState.clear()
	(&RoundRobin{}).Candidates([]model.GroupItem{{ID: 1, ChannelID: 10}, {ID: 2, ChannelID: 11}})
	(&RoundRobin{}).Candidates([]model.GroupItem{{ID: 3, ChannelID: 20}, {ID: 4, ChannelID: 21}})
	InvalidateChannel(10)
	if got := smoothRoundRobinState.len(); got != 1 {
		t.Fatalf("round robin state after invalidation = %d, want 1", got)
	}
}

func TestRuntimeStateInvalidationByAPIKeyAndGroup(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	SetSticky(1, "model-a", 10, 100, "a")
	SetSticky(2, "model-b", 20, 200, "b")
	InvalidateAPIKey(1)
	if GetSticky(1, "model-a", time.Minute) != nil {
		t.Fatal("API key invalidation retained matching sticky state")
	}
	if GetSticky(2, "model-b", time.Minute) == nil {
		t.Fatal("API key invalidation removed unrelated sticky state")
	}

	(&Weighted{}).Candidates([]model.GroupItem{
		{ID: 1, ChannelID: 20, ModelName: "a", Weight: 1},
		{ID: 2, ChannelID: 21, ModelName: "b", Weight: 1},
	})
	InvalidateGroups()
	if globalSession.len() != 0 || smoothWeightedState.len() != 0 {
		t.Fatalf("group invalidation left state: sticky=%d weighted=%d", globalSession.len(), smoothWeightedState.len())
	}
}

func TestRuntimeStateConcurrentAccessAndInvalidation(t *testing.T) {
	resetBalancerRuntimeState()
	defer resetBalancerRuntimeState()

	const workers = 12
	const iterations = 300
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				channelID := (worker+i)%17 + 1
				apiKeyID := worker*iterations + i
				modelName := fmt.Sprintf("model-%d", i%31)
				SetSticky(apiKeyID, modelName, channelID, i+1, modelName)
				_ = GetSticky(apiKeyID, modelName, time.Minute)

				key := circuitKey(channelID, i+1, modelName)
				getOrCreateEntry(key, circuitNow())
				IsTripped(channelID, i+1, modelName)

				(&Weighted{}).Candidates([]model.GroupItem{
					{ID: worker*1000 + i*2 + 1, ChannelID: channelID, ModelName: modelName, Weight: 1},
					{ID: worker*1000 + i*2 + 2, ChannelID: channelID + 100, ModelName: modelName + "-b", Weight: 2},
				})
				if i%23 == 0 {
					InvalidateAPIKey(apiKeyID)
					InvalidateChannel(channelID)
				}
			}
		}()
	}
	wg.Wait()

	if got := globalSession.len(); got > stickyStateLimit {
		t.Fatalf("sticky state exceeded hard limit: %d > %d", got, stickyStateLimit)
	}
	if got := globalBreaker.len(); got > circuitStateLimit {
		t.Fatalf("circuit state exceeded hard limit: %d > %d", got, circuitStateLimit)
	}
	if got := smoothWeightedState.len(); got > weightedStateLimit {
		t.Fatalf("weighted state exceeded hard limit: %d > %d", got, weightedStateLimit)
	}
}
