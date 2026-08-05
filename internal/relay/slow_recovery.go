package relay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
)

// slowRecoveryKey identifies one real upstream candidate without retaining raw
// credentials or query strings. Slow recovery is deliberately scoped more
// narrowly than a channel: a key, model, endpoint, or channel configuration
// version can change independently.
type slowRecoveryKey struct {
	ChannelID        int
	ChannelKeyID     int
	Model            string
	ConfigVersion    int
	ScopeFingerprint string
}

type slowRecoveryEntry struct {
	ConsecutiveTimeouts int
	LastTimeout         time.Time
	LastBudget          time.Duration
	NextAttempt         time.Time
	LastTouched         time.Time
	InFlight            bool
	LeaseUntil          time.Time
	LeaseID             slowRecoveryLeaseID
}

type slowRecoveryLeaseID uint64

type slowRecoveryTracker struct {
	mu          sync.Mutex
	entries     map[slowRecoveryKey]slowRecoveryEntry
	baseBackoff time.Duration
	maxBackoff  time.Duration
	lease       time.Duration
	stateTTL    time.Duration
	maxEntries  int
	nextLeaseID slowRecoveryLeaseID
	now         func() time.Time
}

const (
	slowRecoveryStateTTL   = 24 * time.Hour
	slowRecoveryMaxEntries = 4096
)

func newSlowRecoveryTracker() *slowRecoveryTracker {
	return &slowRecoveryTracker{
		entries:     make(map[slowRecoveryKey]slowRecoveryEntry),
		baseBackoff: 60 * time.Second,
		maxBackoff:  10 * time.Minute,
		lease:       time.Duration(hardMaxInitialResponseTimeoutSeconds) * time.Second,
		stateTTL:    slowRecoveryStateTTL,
		maxEntries:  slowRecoveryMaxEntries,
		now:         time.Now,
	}
}

var globalSlowRecovery = newSlowRecoveryTracker()

func newSlowRecoveryKey(channel *dbmodel.Channel, key dbmodel.ChannelKey, modelName, endpoint string) slowRecoveryKey {
	identity := slowRecoveryKey{
		ChannelKeyID: key.ID,
		Model:        modelName,
	}
	if channel != nil {
		identity.ChannelID = channel.ID
		identity.ConfigVersion = channel.ConfigVersion
		identity.ScopeFingerprint = dbmodel.CapabilityScopeFingerprint(channel, key, endpoint)
	}
	return identity
}

// acquire admits a passive recovery attempt. It never starts network work;
// it only prevents concurrent real user requests from all spending their
// initial-response budget on the same slow candidate.
func (s *slowRecoveryTracker) acquire(key slowRecoveryKey) (allowed bool, leaseID slowRecoveryLeaseID, remaining time.Duration) {
	budget := time.Duration(hardMaxInitialResponseTimeoutSeconds) * time.Second
	if s != nil && s.lease > 0 {
		budget = s.lease
	}
	return s.acquireForBudget(key, budget)
}

// acquireForBudget uses the actual initial-response budget of the selected
// channel for the in-flight lease. An exception channel can legitimately wait
// longer than 120 seconds; its lease must cover that same interval so another
// request cannot start a duplicate recovery attempt while it is still running.
func (s *slowRecoveryTracker) acquireForBudget(key slowRecoveryKey, budget time.Duration) (allowed bool, leaseID slowRecoveryLeaseID, remaining time.Duration) {
	if s == nil || key.ChannelID <= 0 {
		return true, 0, 0
	}
	leaseDuration := boundedChannelInitialResponseBudget(budget)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepExpiredLocked(now)
	entry, ok := s.entries[key]
	if !ok {
		return true, 0, 0
	}
	if !entry.NextAttempt.IsZero() && now.Before(entry.NextAttempt) {
		return false, 0, entry.NextAttempt.Sub(now)
	}
	if entry.InFlight && now.Before(entry.LeaseUntil) {
		return false, 0, entry.LeaseUntil.Sub(now)
	}
	s.nextLeaseID++
	if s.nextLeaseID == 0 {
		s.nextLeaseID++
	}
	entry.InFlight = true
	entry.LeaseUntil = now.Add(leaseDuration)
	entry.LeaseID = s.nextLeaseID
	entry.LastTouched = now
	s.entries[key] = entry
	return true, entry.LeaseID, 0
}

// recordTimeout transitions the candidate into passive recovery. The first
// timeout leaves one immediate real-request recovery opportunity; repeated
// timeouts add exponential backoff. No goroutine or synthetic request is
// created here.
func (s *slowRecoveryTracker) recordTimeout(key slowRecoveryKey, budget time.Duration) {
	s.recordTimeoutForLeaseBudget(key, boundedSlowRecoveryBudget(budget), 0)
}

func (s *slowRecoveryTracker) recordTimeoutForLease(key slowRecoveryKey, budget time.Duration, leaseID slowRecoveryLeaseID) {
	s.recordTimeoutForLeaseBudget(key, boundedSlowRecoveryBudget(budget), leaseID)
}

func (s *slowRecoveryTracker) recordTimeoutForLeaseBudget(key slowRecoveryKey, budget time.Duration, leaseID slowRecoveryLeaseID) {
	if s == nil || key.ChannelID <= 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepExpiredLocked(now)
	entry, exists := s.entries[key]
	if leaseID != 0 && (!exists || entry.LeaseID != leaseID) {
		return
	}
	if entry.LastTouched.IsZero() || s.stateExpired(entry.LastTouched, now) {
		entry = slowRecoveryEntry{}
	}
	entry.ConsecutiveTimeouts++
	entry.LastTimeout = now
	entry.LastBudget = boundedSlowRecoveryBudget(budget)
	entry.LastTouched = now
	if leaseID != 0 || !entry.InFlight || !now.Before(entry.LeaseUntil) {
		entry.InFlight = false
		entry.LeaseUntil = time.Time{}
		entry.LeaseID = 0
	}
	if entry.ConsecutiveTimeouts <= 1 {
		entry.NextAttempt = now
	} else {
		entry.NextAttempt = now.Add(s.backoff(entry.ConsecutiveTimeouts - 1))
	}
	s.storeLocked(key, entry, now)
}

func (s *slowRecoveryTracker) recordSuccess(key slowRecoveryKey) {
	s.recordSuccessForLease(key, 0)
}

func (s *slowRecoveryTracker) recordSuccessForLease(key slowRecoveryKey, leaseID slowRecoveryLeaseID) {
	if s == nil || key.ChannelID <= 0 {
		return
	}
	s.mu.Lock()
	if leaseID != 0 {
		entry, exists := s.entries[key]
		if !exists || entry.LeaseID != leaseID {
			s.mu.Unlock()
			return
		}
	}
	delete(s.entries, key)
	s.mu.Unlock()
}

func (s *slowRecoveryTracker) release(key slowRecoveryKey, leaseID slowRecoveryLeaseID) {
	if s == nil || key.ChannelID <= 0 || leaseID == 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || entry.LeaseID != leaseID {
		return
	}
	entry.InFlight = false
	entry.LeaseUntil = time.Time{}
	entry.LeaseID = 0
	entry.LastTouched = now
	s.entries[key] = entry
}

func (s *slowRecoveryTracker) invalidateChannel(channelID int, modelName string) {
	if s == nil || channelID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.entries {
		if key.ChannelID == channelID && (modelName == "" || key.Model == modelName) {
			delete(s.entries, key)
		}
	}
}

func (s *slowRecoveryTracker) invalidateAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.entries)
	s.mu.Unlock()
}

func (s *slowRecoveryTracker) backoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	shift := attempt - 1
	if shift > 20 {
		shift = 20
	}
	duration := s.baseBackoff * time.Duration(uint64(1)<<shift)
	if duration <= 0 || duration > s.maxBackoff {
		return s.maxBackoff
	}
	return duration
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
		s.sweepExpiredLocked(now)
	}
	for len(s.entries) >= s.maxEntries {
		s.evictOldestLocked()
	}
	s.entries[key] = entry
}

func (s *slowRecoveryTracker) sweepExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if s.stateExpired(entry.LastTouched, now) {
			delete(s.entries, key)
		}
	}
}

func (s *slowRecoveryTracker) stateExpired(lastTouched, now time.Time) bool {
	return lastTouched.IsZero() || (s.stateTTL > 0 && !now.Before(lastTouched.Add(s.stateTTL)))
}

func (s *slowRecoveryTracker) evictOldestLocked() {
	var oldestKey slowRecoveryKey
	var oldest time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.LastTouched.Before(oldest) {
			oldestKey = key
			oldest = entry.LastTouched
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

func slowRecoveryTimeoutBudget(err error) time.Duration {
	if errors.Is(err, errNonStreamRequestTimeout) {
		return boundedSlowRecoveryBudget(time.Duration(hardMaxInitialResponseTimeoutSeconds) * time.Second)
	}
	var timeoutErr *firstTokenTimeoutError
	if errors.As(err, &timeoutErr) {
		if timeoutErr.config.Source == firstTokenTimeoutChannelException {
			return boundedChannelInitialResponseBudget(timeoutErr.config.Duration)
		}
		return boundedSlowRecoveryBudget(timeoutErr.config.Duration)
	}
	return 0
}

func boundedSlowRecoveryBudget(budget time.Duration) time.Duration {
	maxBudget := time.Duration(hardMaxInitialResponseTimeoutSeconds) * time.Second
	if budget <= 0 || budget > maxBudget {
		return maxBudget
	}
	return budget
}

func boundedChannelInitialResponseBudget(budget time.Duration) time.Duration {
	maxBudget := time.Duration(maxChannelFirstTokenTimeoutExceptionSeconds) * time.Second
	if budget <= 0 || budget > maxBudget {
		return time.Duration(hardMaxInitialResponseTimeoutSeconds) * time.Second
	}
	return budget
}

// isSlowRecoveryTimeout identifies initial-response failures that can hide a
// usable but slow candidate. Explicit manual and adaptive timeouts keep their
// existing operator/health semantics instead of entering this state machine.
func isSlowRecoveryTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errNonStreamRequestTimeout) {
		return true
	}
	var timeoutErr *firstTokenTimeoutError
	if !errors.As(err, &timeoutErr) {
		return false
	}
	switch timeoutErr.config.Source {
	case firstTokenTimeoutGlobal,
		firstTokenTimeoutColdStart,
		firstTokenTimeoutNonStreamAttempt,
		firstTokenTimeoutBudget,
		firstTokenTimeoutChannelException:
		return true
	default:
		return false
	}
}

func (ra *relayAttempt) slowRecoveryIdentity() slowRecoveryKey {
	if ra == nil {
		return slowRecoveryKey{}
	}
	if ra.slowRecoveryKey.ChannelID > 0 {
		return ra.slowRecoveryKey
	}
	modelName := ""
	if ra.internalRequest != nil {
		modelName = ra.internalRequest.Model
	}
	if ra.groupItem.ModelName != "" {
		modelName = ra.groupItem.ModelName
	}
	return newSlowRecoveryKey(ra.channel, ra.usedKey, modelName, ra.baseURL)
}

func (ra *relayAttempt) releaseSlowRecoveryLease() {
	if ra == nil || ra.slowRecoveryLease == 0 {
		return
	}
	globalSlowRecovery.release(ra.slowRecoveryKey, ra.slowRecoveryLease)
	ra.slowRecoveryLease = 0
}

func (ra *relayAttempt) recordSlowRecoveryTimeout(err error) {
	if ra == nil || !isSlowRecoveryTimeout(err) {
		return
	}
	identity := ra.slowRecoveryIdentity()
	budget := slowRecoveryTimeoutBudget(err)
	if errors.Is(err, errNonStreamRequestTimeout) && channelHasInitialResponseTimeoutException(ra.channel) {
		budget = channelInitialResponseTimeoutBudget(ra.channel)
	}
	globalSlowRecovery.recordTimeoutForLeaseBudget(identity, budget, ra.slowRecoveryLease)
	ra.slowRecoveryKey = identity
	ra.slowRecoveryLease = 0
}

func (ra *relayAttempt) clearSlowRecoveryState() {
	if ra == nil {
		return
	}
	identity := ra.slowRecoveryIdentity()
	globalSlowRecovery.recordSuccessForLease(identity, ra.slowRecoveryLease)
	ra.slowRecoveryKey = identity
	ra.slowRecoveryLease = 0
}

func slowRecoveryBackoffMessage(remaining time.Duration) string {
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("slow candidate passive recovery backoff, retry after %ds", seconds)
}
