package op

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

func TestAPIKeyCostReserveSerializesLimitedKeyAndCommitsActualCost(t *testing.T) {
	key := isolateAPIKeyCostState(t, model.APIKey{
		ID: 41, Name: "limited", APIKey: "sk-octopus-limited", Enabled: true, MaxCost: 1,
	}, model.StatsAPIKey{
		APIKeyID: 41,
		StatsMetrics: model.StatsMetrics{
			InputCost: 0.4,
		},
	})

	const workers = 64
	start := make(chan struct{})
	results := make(chan struct {
		reservation *APIKeyCostReservation
		err         error
	}, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, reservation, err := APIKeyCostReserve(key.APIKey)
			results <- struct {
				reservation *APIKeyCostReservation
				err         error
			}{reservation: reservation, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var winner *APIKeyCostReservation
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.reservation
			continue
		}
		if !errors.Is(result.err, ErrAPIKeyMaxCostReserved) {
			t.Errorf("APIKeyCostReserve() error = %v, want ErrAPIKeyMaxCostReserved", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent reservations = %d, want 1", successes)
	}

	// Relay metrics are committed before APIKeyAuth returns and runs its defer.
	// The reservation itself never inflates persisted spending.
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{OutputCost: 0.6}); err != nil {
		t.Fatalf("StatsAPIKeyUpdate() error = %v", err)
	}
	winner.Release()
	winner.Release() // idempotent

	stats := StatsAPIKeyGet(key.ID)
	if got := stats.InputCost + stats.OutputCost; got != 1 {
		t.Fatalf("recorded cost = %v, want actual cost 1", got)
	}
	if _, reservation, err := APIKeyCostReserve(key.APIKey); !errors.Is(err, ErrAPIKeyMaxCostReached) || reservation != nil {
		t.Fatalf("reservation at exact limit = (%v, %v), want (nil, ErrAPIKeyMaxCostReached)", reservation, err)
	}
}

func TestAPIKeyCostReserveReleaseAllowsRetryWithoutInventingCost(t *testing.T) {
	key := isolateAPIKeyCostState(t, model.APIKey{
		ID: 42, Name: "retry", APIKey: "sk-octopus-retry", Enabled: true, MaxCost: 5,
	}, model.StatsAPIKey{APIKeyID: 42})

	_, first, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	first.Release()

	_, second, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	second.Release()
	if got := StatsAPIKeyGet(key.ID).InputCost + StatsAPIKeyGet(key.ID).OutputCost; got != 0 {
		t.Fatalf("reservation reported fictitious spending: %v", got)
	}
}

func TestAPIKeyCostReservationCannotPredictSingleResponseOvershoot(t *testing.T) {
	key := isolateAPIKeyCostState(t, model.APIKey{
		ID: 43, Name: "single-response", APIKey: "sk-octopus-single-response", Enabled: true, MaxCost: 1,
	}, model.StatsAPIKey{
		APIKeyID:     43,
		StatsMetrics: model.StatsMetrics{InputCost: 0.9},
	})

	_, reservation, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("reservation with remaining budget: %v", err)
	}
	// Usage is reported only after the upstream response. A single response can
	// therefore exceed the remaining 0.1 even though no concurrent request was
	// allowed to share that budget. The next request must be rejected.
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{OutputCost: 0.5}); err != nil {
		t.Fatalf("StatsAPIKeyUpdate() error = %v", err)
	}
	reservation.Release()
	if got := StatsAPIKeyGet(key.ID).InputCost + StatsAPIKeyGet(key.ID).OutputCost; math.Abs(got-1.4) > 1e-12 {
		t.Fatalf("actual post-response cost = %v, want 1.4", got)
	}
	if _, next, err := APIKeyCostReserve(key.APIKey); !errors.Is(err, ErrAPIKeyMaxCostReached) || next != nil {
		t.Fatalf("reservation after single-response overshoot = (%v, %v), want max-cost rejection", next, err)
	}
}

func TestAPIKeyUpdateToLimitedAccountsForAlreadyActiveRequests(t *testing.T) {
	isolateAPIKeyCostState(t, model.APIKey{})
	initTestDB(t)
	key := model.APIKey{
		Name: "update-limit", APIKey: "sk-octopus-update-limit", Enabled: true,
	}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() error = %v", err)
	}

	_, active, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("unlimited reservation: %v", err)
	}
	key.MaxCost = 10
	if err := APIKeyUpdate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyUpdate() error = %v", err)
	}

	if _, reservation, err := APIKeyCostReserve(key.APIKey); !errors.Is(err, ErrAPIKeyMaxCostReserved) || reservation != nil {
		t.Fatalf("reservation while pre-update request is active = (%v, %v), want reserved", reservation, err)
	}
	active.Release()
	_, next, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("reservation after active request release: %v", err)
	}
	next.Release()
}

func TestAPIKeyDeleteWhileReservedPreventsStatsResurrection(t *testing.T) {
	isolateAPIKeyCostState(t, model.APIKey{})
	initTestDB(t)
	key := model.APIKey{
		Name: "delete-reserved", APIKey: "sk-octopus-delete-reserved", Enabled: true, MaxCost: 10,
	}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() error = %v", err)
	}

	_, active, err := APIKeyCostReserve(key.APIKey)
	if err != nil {
		t.Fatalf("APIKeyCostReserve() error = %v", err)
	}
	if err := APIKeyDelete(key.ID, context.Background()); err != nil {
		t.Fatalf("APIKeyDelete() error = %v", err)
	}
	if err := StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 3}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StatsAPIKeyUpdate() after deletion error = %v, want ErrNotFound", err)
	}
	active.Release()

	if _, reservation, err := APIKeyCostReserve(key.APIKey); !errors.Is(err, ErrNotFound) || reservation != nil {
		t.Fatalf("deleted-key reservation = (%v, %v), want ErrNotFound", reservation, err)
	}
	if _, ok := statsService.apiKeys.Get(key.ID); ok {
		t.Fatal("deleted API key stats were recreated")
	}
	mu := statsLockFor(&statsService.apiKeyUpdateLocks, key.ID)
	mu.Lock()
	_, stateExists := statsService.getAPIKeyRequestStateLocked(key.ID)
	mu.Unlock()
	if stateExists {
		t.Fatal("released deleted-key reservation state was retained")
	}
}

func isolateAPIKeyCostState(t *testing.T, key model.APIKey, stats ...model.StatsAPIKey) model.APIKey {
	t.Helper()
	oldStatsService := statsService
	oldAPIKeyCache := apiKeyCache
	oldAPIKeyIDMap := apiKeyIDMap
	statsService = NewStatsService()
	apiKeyCache = cache.New[int, model.APIKey](16)
	apiKeyIDMap = cache.New[string, int](16)
	t.Cleanup(func() {
		statsService = oldStatsService
		apiKeyCache = oldAPIKeyCache
		apiKeyIDMap = oldAPIKeyIDMap
	})

	if key.ID > 0 {
		apiKeyCache.Set(key.ID, key)
		apiKeyIDMap.Set(key.APIKey, key.ID)
	}
	for _, value := range stats {
		statsService.apiKeys.Set(value.APIKeyID, value)
	}
	return key
}
