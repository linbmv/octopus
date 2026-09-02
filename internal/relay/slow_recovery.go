package relay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

var (
	errUpstreamNonStreamResponseTimeout = errors.New("upstream non-stream response timeout")
	errUpstreamStreamFirstEventTimeout  = errors.New("upstream stream first event timeout")
)

// slowRecoveryKey identifies a real candidate without retaining credentials or
// request content. The URL is included because two endpoints of one channel
// can have different latency characteristics.
type slowRecoveryKey struct {
	channelID int
	keyID     int
	model     string
	baseURL   string
}

type slowRecoveryEntry struct {
	timeouts    int
	nextAttempt time.Time
	lastTouched time.Time
	inFlight    bool
	leaseUntil  time.Time
	leaseID     uint64
}

type slowRecoveryTracker struct {
	mu          sync.Mutex
	entries     map[slowRecoveryKey]slowRecoveryEntry
	baseBackoff time.Duration
	maxBackoff  time.Duration
	stateTTL    time.Duration
	maxEntries  int
	now         func() time.Time
	nextLeaseID uint64
}

const (
	maxSlowRecoveryLease   = 10 * time.Minute
	slowRecoveryStateTTL   = 24 * time.Hour
	slowRecoveryMaxEntries = 4096
)

func newSlowRecoveryTracker(now func() time.Time) *slowRecoveryTracker {
	if now == nil {
		now = time.Now
	}
	return &slowRecoveryTracker{
		entries:     make(map[slowRecoveryKey]slowRecoveryEntry),
		baseBackoff: time.Minute,
		maxBackoff:  10 * time.Minute,
		stateTTL:    slowRecoveryStateTTL,
		maxEntries:  slowRecoveryMaxEntries,
		now:         now,
	}
}

var globalSlowRecovery = newSlowRecoveryTracker(time.Now)

func newSlowRecoveryKey(channelID, keyID int, modelName, baseURL string) slowRecoveryKey {
	return slowRecoveryKey{channelID: channelID, keyID: keyID, model: modelName, baseURL: baseURL}
}

func (s *slowRecoveryTracker) acquire(key slowRecoveryKey, budget time.Duration) (bool, uint64, time.Duration) {
	if s == nil || key.channelID <= 0 {
		return true, 0, 0
	}
	now := s.now()
	lease := budget
	if lease <= 0 || lease > maxSlowRecoveryLease {
		lease = maxSlowRecoveryLease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	entry, exists := s.entries[key]
	if !exists {
		return true, 0, 0
	}
	if now.Before(entry.nextAttempt) {
		return false, 0, entry.nextAttempt.Sub(now)
	}
	if entry.inFlight && now.Before(entry.leaseUntil) {
		return false, 0, entry.leaseUntil.Sub(now)
	}
	s.nextLeaseID++
	if s.nextLeaseID == 0 {
		s.nextLeaseID++
	}
	entry.inFlight = true
	entry.leaseUntil = now.Add(lease)
	entry.leaseID = s.nextLeaseID
	entry.lastTouched = now
	s.entries[key] = entry
	return true, entry.leaseID, 0
}

func (s *slowRecoveryTracker) recordTimeout(key slowRecoveryKey, leaseID uint64) {
	if s == nil || key.channelID <= 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	entry, exists := s.entries[key]
	if leaseID != 0 && (!exists || entry.leaseID != leaseID) {
		return
	}
	entry.timeouts++
	entry.lastTouched = now
	entry.inFlight = false
	entry.leaseUntil = time.Time{}
	entry.leaseID = 0
	if entry.timeouts == 1 {
		// Give the next real request one immediate recovery opportunity.
		entry.nextAttempt = now
	} else {
		entry.nextAttempt = now.Add(s.backoff(entry.timeouts - 1))
	}
	s.storeLocked(key, entry, now)
}

func (s *slowRecoveryTracker) recordSuccess(key slowRecoveryKey, leaseID uint64) {
	if s == nil || key.channelID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaseID != 0 {
		entry, exists := s.entries[key]
		if !exists || entry.leaseID != leaseID {
			return
		}
	}
	delete(s.entries, key)
}

func (s *slowRecoveryTracker) release(key slowRecoveryKey, leaseID uint64) {
	if s == nil || key.channelID <= 0 || leaseID == 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists || entry.leaseID != leaseID {
		return
	}
	entry.inFlight = false
	entry.leaseUntil = time.Time{}
	entry.leaseID = 0
	entry.lastTouched = now
	s.entries[key] = entry
}

func (s *slowRecoveryTracker) invalidateChannel(channelID int, modelName string) {
	if s == nil || channelID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.entries {
		if key.channelID == channelID && (modelName == "" || key.model == modelName) {
			delete(s.entries, key)
		}
	}
}

func (s *slowRecoveryTracker) backoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	shift := min(attempt-1, 20)
	backoff := s.baseBackoff * time.Duration(uint64(1)<<shift)
	if backoff <= 0 || backoff > s.maxBackoff {
		return s.maxBackoff
	}
	return backoff
}

func (s *slowRecoveryTracker) storeLocked(key slowRecoveryKey, entry slowRecoveryEntry, now time.Time) {
	if s.maxEntries <= 0 {
		return
	}
	if _, exists := s.entries[key]; exists {
		s.entries[key] = entry
		return
	}
	if len(s.entries) >= s.maxEntries {
		s.sweepLocked(now)
	}
	for len(s.entries) >= s.maxEntries {
		var oldestKey slowRecoveryKey
		var oldest time.Time
		first := true
		for candidateKey, candidate := range s.entries {
			if first || candidate.lastTouched.Before(oldest) {
				oldestKey, oldest, first = candidateKey, candidate.lastTouched, false
			}
		}
		if first {
			break
		}
		delete(s.entries, oldestKey)
	}
	s.entries[key] = entry
}

func (s *slowRecoveryTracker) sweepLocked(now time.Time) {
	if s.stateTTL <= 0 {
		return
	}
	for key, entry := range s.entries {
		if entry.lastTouched.IsZero() || !now.Before(entry.lastTouched.Add(s.stateTTL)) {
			delete(s.entries, key)
		}
	}
}

func slowRecoveryBudget(group model.Group, streaming bool) time.Duration {
	seconds := group.RelayConfig.MemberNonStreamResponseTimeoutSeconds
	if streaming {
		seconds = group.RelayConfig.MemberStreamFirstEventTimeoutSeconds
	}
	if seconds <= 0 {
		seconds = model.DefaultGroupRelayConfig().MemberNonStreamResponseTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func isSlowRecoveryTimeout(err error) bool {
	return errors.Is(err, errUpstreamNonStreamResponseTimeout) || errors.Is(err, errUpstreamStreamFirstEventTimeout)
}

func slowRecoveryBackoffMessage(remaining time.Duration) string {
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("slow candidate passive recovery backoff, retry after %ds", seconds)
}
