package relay

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
)

// circuitState is deliberately independent from RouteState. RouteState is a
// per-request/group failover aid; this state survives across requests and is
// scoped to one concrete channel credential and model.
type circuitState uint8

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

type circuitKey struct {
	channelID int
	keyID     int
	model     string
}

type circuitEntry struct {
	state               circuitState
	consecutiveFailures int
	tripCount           int
	lastFailure         time.Time
	lastTouched         time.Time
	probeCount          int
	probeLeaseUntil     time.Time
	nextProbeID         uint64
	probeIDs            map[uint64]struct{}
}

// circuitPermit identifies a half-open lease. A generation prevents a late
// completion from an older concurrent probe from changing a newer state.
type circuitPermit struct {
	key        circuitKey
	generation uint64
}

type circuitConfig struct {
	threshold    int
	baseCooldown time.Duration
	maxCooldown  time.Duration
	maxProbes    int
	probeLease   time.Duration
	stateTTL     time.Duration
	maxEntries   int
}

func defaultCircuitConfig() circuitConfig {
	return circuitConfig{
		threshold:    2,
		baseCooldown: time.Minute,
		maxCooldown:  10 * time.Minute,
		maxProbes:    2,
		probeLease:   time.Minute,
		stateTTL:     24 * time.Hour,
		maxEntries:   16384,
	}
}

type circuitBreaker struct {
	mu      sync.Mutex
	entries map[circuitKey]circuitEntry
	now     func() time.Time
}

func newCircuitBreaker(now func() time.Time) *circuitBreaker {
	if now == nil {
		now = time.Now
	}
	return &circuitBreaker{
		entries: make(map[circuitKey]circuitEntry),
		now:     now,
	}
}

var globalCircuitBreaker = newCircuitBreaker(time.Now)

const maxConfiguredCircuitDuration = 30 * 24 * time.Hour

func configuredCircuitDuration(key model.SettingKey, fallback time.Duration) time.Duration {
	value, err := op.SettingGetInt(key)
	if err != nil || value <= 0 {
		return fallback
	}
	if value > int(maxConfiguredCircuitDuration/time.Second) {
		return maxConfiguredCircuitDuration
	}
	duration := time.Duration(value) * time.Second
	if duration <= 0 || duration > maxConfiguredCircuitDuration {
		return maxConfiguredCircuitDuration
	}
	return duration
}

func configuredCircuitConfig() circuitConfig {
	cfg := defaultCircuitConfig()
	if value, err := op.SettingGetInt(model.SettingKeyCircuitBreakerThreshold); err == nil && value > 0 {
		cfg.threshold = value
	}
	cfg.baseCooldown = configuredCircuitDuration(model.SettingKeyCircuitBreakerCooldown, cfg.baseCooldown)
	cfg.maxCooldown = configuredCircuitDuration(model.SettingKeyCircuitBreakerMaxCooldown, cfg.maxCooldown)
	if cfg.maxCooldown < cfg.baseCooldown {
		cfg.maxCooldown = cfg.baseCooldown
	}
	if value, err := op.SettingGetInt(model.SettingKeyCircuitBreakerHalfOpenProbes); err == nil && value > 0 {
		cfg.maxProbes = min(value, 64)
	}
	cfg.probeLease = configuredCircuitDuration(model.SettingKeyCircuitBreakerProbeLease, cfg.probeLease)
	return cfg
}

func circuitCooldown(cfg circuitConfig, tripCount int) time.Duration {
	if tripCount < 1 {
		tripCount = 1
	}
	shift := tripCount - 1
	if shift > 20 {
		shift = 20
	}
	cooldown := cfg.baseCooldown * time.Duration(uint64(1)<<shift)
	if cooldown <= 0 || cooldown > cfg.maxCooldown {
		return cfg.maxCooldown
	}
	return cooldown
}

// allow returns whether a concrete channel/key/model may be contacted. The
// permit is non-zero only for a half-open probe and must be settled or aborted
// by the caller exactly once.
func (b *circuitBreaker) allow(key circuitKey, cfg circuitConfig) (bool, circuitPermit, time.Duration) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(now, cfg)

	entry, exists := b.entries[key]
	if !exists {
		return true, circuitPermit{}, 0
	}
	entry.lastTouched = now
	switch entry.state {
	case circuitClosed:
		b.entries[key] = entry
		return true, circuitPermit{}, 0
	case circuitOpen:
		remaining := circuitCooldown(cfg, entry.tripCount) - now.Sub(entry.lastFailure)
		if remaining > 0 {
			b.entries[key] = entry
			return false, circuitPermit{}, remaining
		}
		entry.state = circuitHalfOpen
		entry.probeIDs = make(map[uint64]struct{})
		entry.probeCount = 0
		entry.probeLeaseUntil = now.Add(cfg.probeLease)
		permit := b.admitProbeLocked(key, &entry, now, cfg)
		b.entries[key] = entry
		return true, permit, 0
	case circuitHalfOpen:
		if entry.probeCount < cfg.maxProbes {
			permit := b.admitProbeLocked(key, &entry, now, cfg)
			b.entries[key] = entry
			return true, permit, 0
		}
		if !now.Before(entry.probeLeaseUntil) {
			// A caller can disappear after admission. Replacing the expired
			// lease prevents a permanently stuck half-open entry.
			entry.probeIDs = make(map[uint64]struct{})
			entry.probeCount = 0
			entry.probeLeaseUntil = now.Add(cfg.probeLease)
			permit := b.admitProbeLocked(key, &entry, now, cfg)
			b.entries[key] = entry
			return true, permit, 0
		}
		b.entries[key] = entry
		return false, circuitPermit{}, entry.probeLeaseUntil.Sub(now)
	default:
		entry.state = circuitClosed
		entry.consecutiveFailures = 0
		entry.probeCount = 0
		b.entries[key] = entry
		return true, circuitPermit{}, 0
	}
}

func (b *circuitBreaker) admitProbeLocked(key circuitKey, entry *circuitEntry, now time.Time, cfg circuitConfig) circuitPermit {
	if entry.probeIDs == nil {
		entry.probeIDs = make(map[uint64]struct{})
	}
	entry.nextProbeID++
	if entry.nextProbeID == 0 {
		entry.nextProbeID++
	}
	entry.probeIDs[entry.nextProbeID] = struct{}{}
	entry.probeCount = len(entry.probeIDs)
	entry.probeLeaseUntil = now.Add(cfg.probeLease)
	return circuitPermit{key: key, generation: entry.nextProbeID}
}

func (b *circuitBreaker) recordSuccess(key circuitKey, permit circuitPermit) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, exists := b.entries[key]
	if !exists {
		return
	}
	if permit.generation != 0 &&
		(entry.state != circuitHalfOpen || !entry.hasProbe(permit.generation)) {
		return
	}
	// A normal request admitted while the breaker was Closed may finish after
	// another request has opened it. Its late success is not evidence that the
	// currently blocked state recovered.
	if permit.generation == 0 && entry.state != circuitClosed {
		return
	}
	entry.state = circuitClosed
	entry.consecutiveFailures = 0
	entry.tripCount = 0
	entry.probeCount = 0
	entry.probeLeaseUntil = time.Time{}
	clear(entry.probeIDs)
	entry.lastTouched = now
	b.entries[key] = entry
}

func (b *circuitBreaker) recordFailure(key circuitKey, permit circuitPermit, cfg circuitConfig) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(now, cfg)
	entry := b.entries[key]
	if permit.generation != 0 {
		if entry.state != circuitHalfOpen || !entry.hasProbe(permit.generation) {
			return
		}
		entry.state = circuitOpen
		entry.tripCount++
		entry.consecutiveFailures = 0
		entry.probeCount = 0
		entry.probeLeaseUntil = time.Time{}
		clear(entry.probeIDs)
		entry.lastFailure = now
		entry.lastTouched = now
		b.entries[key] = entry
		return
	}
	if entry.state != circuitClosed {
		return
	}
	entry.consecutiveFailures++
	entry.lastFailure = now
	entry.lastTouched = now
	if entry.consecutiveFailures >= cfg.threshold {
		entry.state = circuitOpen
		entry.tripCount++
	}
	b.storeLocked(key, entry, cfg, now)
}

func (b *circuitBreaker) abort(permit circuitPermit) {
	if permit.generation == 0 {
		return
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, exists := b.entries[permit.key]
	if !exists || entry.state != circuitHalfOpen || !entry.hasProbe(permit.generation) {
		return
	}
	delete(entry.probeIDs, permit.generation)
	entry.probeCount = len(entry.probeIDs)
	entry.lastTouched = now
	b.entries[permit.key] = entry
}

func (entry circuitEntry) hasProbe(id uint64) bool {
	_, ok := entry.probeIDs[id]
	return ok
}

func (b *circuitBreaker) storeLocked(key circuitKey, entry circuitEntry, cfg circuitConfig, now time.Time) {
	if cfg.maxEntries <= 0 {
		return
	}
	if _, exists := b.entries[key]; exists {
		b.entries[key] = entry
		return
	}
	if len(b.entries) >= cfg.maxEntries {
		b.sweepLocked(now, cfg)
	}
	for len(b.entries) >= cfg.maxEntries {
		oldestKey := key
		var oldest time.Time
		found := false
		for candidateKey, candidateEntry := range b.entries {
			if !found || candidateEntry.lastTouched.Before(oldest) {
				oldestKey = candidateKey
				oldest = candidateEntry.lastTouched
				found = true
			}
		}
		if !found {
			break
		}
		delete(b.entries, oldestKey)
	}
	b.entries[key] = entry
}

func (b *circuitBreaker) sweepLocked(now time.Time, cfg circuitConfig) {
	if cfg.stateTTL <= 0 {
		return
	}
	for key, entry := range b.entries {
		if entry.lastTouched.IsZero() || !now.Before(entry.lastTouched.Add(cfg.stateTTL)) {
			delete(b.entries, key)
		}
	}
}

func (b *circuitBreaker) resetChannel(channelID int, modelName string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key := range b.entries {
		if key.channelID == channelID && (modelName == "" || key.model == modelName) {
			delete(b.entries, key)
		}
	}
}

// tryCircuit is the production entry point. It is intentionally private to
// relay so diagnostics cannot mutate production breaker state accidentally.
func tryCircuit(channelID, keyID int, modelName string) (bool, circuitPermit, time.Duration) {
	key := circuitKey{channelID: channelID, keyID: keyID, model: modelName}
	return globalCircuitBreaker.allow(key, configuredCircuitConfig())
}

func recordCircuitSuccess(channelID, keyID int, modelName string, permit circuitPermit) {
	globalCircuitBreaker.recordSuccess(circuitKey{channelID: channelID, keyID: keyID, model: modelName}, permit)
}

func recordCircuitFailure(channelID, keyID int, modelName string, permit circuitPermit) {
	globalCircuitBreaker.recordFailure(
		circuitKey{channelID: channelID, keyID: keyID, model: modelName},
		permit,
		configuredCircuitConfig(),
	)
}

func abortCircuitProbe(permit circuitPermit) {
	globalCircuitBreaker.abort(permit)
}

func resetChannelCircuit(channelID int, modelName string) {
	globalCircuitBreaker.resetChannel(channelID, modelName)
}

// InvalidateChannelRuntimeState drops runtime-only breaker and slow-candidate
// evidence after an administrator changes a channel, key, endpoint, or model.
// Persisted configuration is the source of truth; old runtime penalties must
// not survive a deliberate replacement of that configuration.
func InvalidateChannelRuntimeState(channelID int, modelName string) {
	resetChannelCircuit(channelID, modelName)
	globalSlowRecovery.invalidateChannel(channelID, modelName)
}

// CircuitSnapshotForChannel returns only non-Closed breaker entries for the
// authenticated administration API. No credential material is included.
func CircuitSnapshotForChannel(channelID int) []model.ChannelCircuitStatus {
	cfg := configuredCircuitConfig()
	now := globalCircuitBreaker.now()
	globalCircuitBreaker.mu.Lock()
	defer globalCircuitBreaker.mu.Unlock()
	globalCircuitBreaker.sweepLocked(now, cfg)

	status := make([]model.ChannelCircuitStatus, 0)
	for key, entry := range globalCircuitBreaker.entries {
		if key.channelID != channelID || entry.state == circuitClosed {
			continue
		}
		item := model.ChannelCircuitStatus{
			ChannelID:           key.channelID,
			ChannelKeyID:        key.keyID,
			ModelName:           key.model,
			ConsecutiveFailures: entry.consecutiveFailures,
			TripCount:           entry.tripCount,
			InFlightProbes:      entry.probeCount,
		}
		switch entry.state {
		case circuitOpen:
			item.State = "open"
			remaining := circuitCooldown(cfg, entry.tripCount) - now.Sub(entry.lastFailure)
			if remaining > 0 {
				item.RemainingCooldownSeconds = int((remaining + time.Second - 1) / time.Second)
			}
		case circuitHalfOpen:
			item.State = "half_open"
		}
		status = append(status, item)
	}
	sort.Slice(status, func(i, j int) bool {
		if status[i].ModelName != status[j].ModelName {
			return status[i].ModelName < status[j].ModelName
		}
		return status[i].ChannelKeyID < status[j].ChannelKeyID
	})
	return status
}

type circuitUnavailableError struct {
	channelID int
	model     string
	remaining time.Duration
}

func (e *circuitUnavailableError) Error() string {
	if e.remaining > 0 {
		return fmt.Sprintf("channel %d model %q is circuit-open; retry after %ds", e.channelID, e.model, int((e.remaining+time.Second-1)/time.Second))
	}
	return fmt.Sprintf("channel %d model %q has no circuit-eligible credential", e.channelID, e.model)
}
