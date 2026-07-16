package op

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/bestruirui/octopus/internal/model"
)

var (
	// ErrAPIKeyMaxCostReached is stable because API clients receive this text.
	ErrAPIKeyMaxCostReached = errors.New("API key has reached the max cost")
	// ErrAPIKeyMaxCostReserved tells a caller to retry after the in-flight
	// request has recorded its actual cost and released the reservation.
	ErrAPIKeyMaxCostReserved = errors.New("API key max cost budget is reserved by an in-flight request")
)

type apiKeyRequestState struct {
	active  int
	deleted bool
}

// APIKeyCostReservation represents one authenticated API-key request. Release
// is idempotent so it is safe to defer across normal completion, cancellation,
// and panic unwinding.
type APIKeyCostReservation struct {
	service  *StatsService
	apiKeyID int
	once     sync.Once
}

// APIKeyCostReserve atomically resolves the current key configuration, checks
// its recorded cost, and registers an in-flight request.
//
// Actual response cost is unknowable until the upstream reports usage. Rather
// than add a fictitious amount to persisted spending, a limited key
// conservatively reserves all of its remaining budget: only one request may be
// in flight for that key. This closes the concurrent check-then-act gap. It
// cannot guarantee that a single response will not itself cost more than the
// remaining budget; callers must not present this as an absolute monetary cap.
func APIKeyCostReserve(secret string) (model.APIKey, *APIKeyCostReservation, error) {
	return apiKeyCostAccess(secret, true)
}

// APIKeyCostCheck applies the same atomic configured-cost check without
// reserving budget. It is intended for authenticated read-only endpoints such
// as model discovery and API-key statistics, which cannot add relay cost and
// therefore need not contend with an in-flight generation.
func APIKeyCostCheck(secret string) (model.APIKey, error) {
	key, _, err := apiKeyCostAccess(secret, false)
	return key, err
}

func apiKeyCostAccess(secret string, reserve bool) (model.APIKey, *APIKeyCostReservation, error) {
	id, ok := apiKeyIDMap.Get(secret)
	if !ok {
		return model.APIKey{}, nil, fmt.Errorf("%w: API key not found", ErrNotFound)
	}

	service := statsService
	mu := statsLockFor(&service.apiKeyUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()

	key, ok := apiKeyCache.Get(id)
	// Recheck the immutable secret after taking the ID lock. APIKeyDelete may
	// have removed the cache entry between the reverse-index lookup and here.
	if !ok || key.APIKey != secret {
		return model.APIKey{}, nil, fmt.Errorf("%w: API key not found", ErrNotFound)
	}

	if key.MaxCost > 0 {
		stats, _ := service.apiKeys.Get(id)
		spent := stats.InputCost + stats.OutputCost
		// Corrupt non-finite persisted values fail closed. Validated writes never
		// create them, but allowing a request here would bypass the configured cap.
		if math.IsNaN(spent) || math.IsInf(spent, 0) || spent < 0 || spent >= key.MaxCost {
			return key, nil, ErrAPIKeyMaxCostReached
		}
		state := service.apiKeyRequestStateLocked(id)
		if reserve && state.active > 0 {
			return key, nil, ErrAPIKeyMaxCostReserved
		}
	}
	if !reserve {
		return key, nil, nil
	}

	state := service.apiKeyRequestStateLocked(id)
	state.active++
	service.setAPIKeyRequestStateLocked(id, state)
	return key, &APIKeyCostReservation{service: service, apiKeyID: id}, nil
}

// Release gives back only the in-memory reservation. Actual spending is
// recorded independently by StatsAPIKeyUpdate before the relay handler returns.
func (r *APIKeyCostReservation) Release() {
	if r == nil || r.service == nil {
		return
	}
	r.once.Do(func() {
		mu := statsLockFor(&r.service.apiKeyUpdateLocks, r.apiKeyID)
		mu.Lock()
		defer mu.Unlock()

		state, ok := r.service.getAPIKeyRequestStateLocked(r.apiKeyID)
		if !ok {
			return
		}
		if state.active > 0 {
			state.active--
		}
		if state.active == 0 {
			r.service.deleteAPIKeyRequestStateLocked(r.apiKeyID)
			return
		}
		r.service.setAPIKeyRequestStateLocked(r.apiKeyID, state)
	})
}

func (s *StatsService) apiKeyRequestShard(id int) int {
	return int(uint(id) & uint(len(s.apiKeyRequests)-1))
}

// The helpers below intentionally do not lock. Their Locked suffix documents
// the invariant and lets key CRUD and stats updates compose them atomically.
func (s *StatsService) getAPIKeyRequestStateLocked(id int) (apiKeyRequestState, bool) {
	shard := s.apiKeyRequests[s.apiKeyRequestShard(id)]
	if shard == nil {
		return apiKeyRequestState{}, false
	}
	state, ok := shard[id]
	return state, ok
}

func (s *StatsService) apiKeyRequestStateLocked(id int) apiKeyRequestState {
	state, _ := s.getAPIKeyRequestStateLocked(id)
	return state
}

func (s *StatsService) setAPIKeyRequestStateLocked(id int, state apiKeyRequestState) {
	index := s.apiKeyRequestShard(id)
	if s.apiKeyRequests[index] == nil {
		s.apiKeyRequests[index] = make(map[int]apiKeyRequestState)
	}
	s.apiKeyRequests[index][id] = state
}

func (s *StatsService) deleteAPIKeyRequestStateLocked(id int) {
	index := s.apiKeyRequestShard(id)
	delete(s.apiKeyRequests[index], id)
}

func (s *StatsService) apiKeyDeletedLocked(id int) bool {
	state, ok := s.getAPIKeyRequestStateLocked(id)
	return ok && state.deleted
}

func (s *StatsService) markAPIKeyDeletedLocked(id int) {
	state := s.apiKeyRequestStateLocked(id)
	state.deleted = true
	if state.active == 0 {
		s.deleteAPIKeyRequestStateLocked(id)
		return
	}
	s.setAPIKeyRequestStateLocked(id, state)
}

func (s *StatsService) resetAPIKeyRequestStateLocked(id int) {
	s.deleteAPIKeyRequestStateLocked(id)
}
