package relay

import (
	"math"
	"net/url"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

type runtimeURLKey struct {
	ChannelID int
	URL       string
}

type runtimeURLLatency struct {
	ValueMS  float64
	LastSeen time.Time
}

type runtimeURLCooldown struct {
	Until            time.Time
	ConsecutiveFails int
	LastTouched      time.Time
}

type runtimeURLCandidate struct {
	URL     string
	Latency float64
	Known   bool
	Cooled  bool
	Until   time.Time
}

// RuntimeURLStatus is the operationally safe URL state exposed to the
// management API. It deliberately omits provider errors and authentication
// material that may be present in a configured URL.
type RuntimeURLStatus struct {
	URL                      string `json:"url"`
	Rank                     int    `json:"rank"`
	Known                    bool   `json:"known"`
	LatencyMS                int    `json:"latency_ms,omitempty"`
	CooldownRemainingSeconds int    `json:"cooldown_remaining_seconds,omitempty"`
	Cooled                   bool   `json:"cooled"`
	SelectionReason          string `json:"selection_reason"`
}

type runtimeURLSelector struct {
	mu           sync.RWMutex
	latencies    map[runtimeURLKey]runtimeURLLatency
	cooldowns    map[runtimeURLKey]runtimeURLCooldown
	alpha        float64
	cooldownBase time.Duration
	cooldownMax  time.Duration
	stateTTL     time.Duration
	maxEntries   int
	sweepEvery   uint64
	writeCount   uint64
	now          func() time.Time
}

const (
	runtimeURLStateTTL        = 24 * time.Hour
	runtimeURLStateMaxEntries = 4096
	runtimeURLStateSweepEvery = 64
)

func newRuntimeURLSelector() *runtimeURLSelector {
	return &runtimeURLSelector{
		latencies:    make(map[runtimeURLKey]runtimeURLLatency),
		cooldowns:    make(map[runtimeURLKey]runtimeURLCooldown),
		alpha:        0.3,
		cooldownBase: 2 * time.Minute,
		cooldownMax:  30 * time.Minute,
		stateTTL:     runtimeURLStateTTL,
		maxEntries:   runtimeURLStateMaxEntries,
		sweepEvery:   runtimeURLStateSweepEvery,
		now:          time.Now,
	}
}

var globalRuntimeURLSelector = newRuntimeURLSelector()

func selectRuntimeBaseURL(channel *model.Channel) string {
	if channel == nil {
		return ""
	}
	return globalRuntimeURLSelector.Select(channel.ID, channel.BaseUrls)
}

func runtimeBaseURLCandidates(channel *model.Channel) []string {
	if channel == nil {
		return nil
	}
	return globalRuntimeURLSelector.Candidates(channel.ID, channel.BaseUrls)
}

func recordRuntimeURLSuccess(channelID int, baseURL string, duration time.Duration) {
	globalRuntimeURLSelector.RecordSuccess(channelID, baseURL, duration)
}

func recordRuntimeURLFailure(channelID int, baseURL string) {
	globalRuntimeURLSelector.RecordFailure(channelID, baseURL)
}

// InvalidateRuntimeURLState removes cached latency and cooldown state for a
// channel. Channel update/delete handlers should call it after the persistent
// change succeeds so stale URLs cannot influence subsequent routing.
func InvalidateRuntimeURLState(channelID int) {
	globalRuntimeURLSelector.InvalidateChannel(channelID)
	globalChannelRateLimits.invalidate(channelID)
}

// InvalidateAllRuntimeState clears endpoint, rate-limit and adaptive-health
// evidence after a bulk database restore.
func InvalidateAllRuntimeState() {
	globalRuntimeURLSelector.InvalidateAll()
	globalChannelRateLimits.clear()
	globalChannelRPMLimiter.clear()
	if healthManager != nil {
		healthManager.InvalidateAll()
	}
}

func (s *runtimeURLSelector) Select(channelID int, baseURLs []model.BaseUrl) string {
	candidates := s.Candidates(channelID, baseURLs)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// Candidates returns a stable, health-aware endpoint order for one channel.
// The caller may walk this list during a single request without selecting an
// endpoint again and accidentally repeating a failed URL.
func (s *runtimeURLSelector) Candidates(channelID int, baseURLs []model.BaseUrl) []string {
	urls := compactBaseURLs(baseURLs)
	if len(urls) == 0 {
		return nil
	}
	if len(urls) == 1 {
		return urls
	}

	now := s.now()
	configuredLatency := make(map[string]float64, len(baseURLs))
	for _, baseURL := range baseURLs {
		if baseURL.URL != "" && baseURL.Delay > 0 {
			configuredLatency[baseURL.URL] = float64(baseURL.Delay)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	candidates := make([]runtimeURLCandidate, 0, len(urls))
	for _, url := range urls {
		key := runtimeURLKey{ChannelID: channelID, URL: url}
		item := runtimeURLCandidate{URL: url}
		if latency, ok := s.latencies[key]; ok && !runtimeURLStateExpired(latency.LastSeen, now, s.stateTTL) {
			item.Latency = latency.ValueMS
			item.Known = true
		} else if latency := configuredLatency[url]; latency > 0 {
			item.Latency = latency
			item.Known = true
		}
		if cooldown, ok := s.cooldowns[key]; ok &&
			!runtimeURLStateExpired(cooldown.LastTouched, now, s.stateTTL) &&
			now.Before(cooldown.Until) {
			item.Cooled = true
			item.Until = cooldown.Until
		}
		candidates = append(candidates, item)
	}

	available := make([]runtimeURLCandidate, 0, len(candidates))
	cooled := make([]runtimeURLCandidate, 0)
	for _, candidate := range candidates {
		if candidate.Cooled {
			cooled = append(cooled, candidate)
			continue
		}
		available = append(available, candidate)
	}
	if len(available) == 0 {
		available = earliestCooldownCandidates(cooled)
	}
	if len(available) == 0 {
		return nil
	}

	unknown := make([]runtimeURLCandidate, 0)
	known := make([]runtimeURLCandidate, 0, len(available))
	for _, candidate := range available {
		if !candidate.Known {
			unknown = append(unknown, candidate)
			continue
		}
		known = append(known, candidate)
	}
	ordered := make([]string, 0, len(available))
	// Unknown endpoints are deliberately explored first, but retain configured
	// order so one request cannot oscillate randomly between URLs. Once samples
	// exist, the known endpoints follow by measured/configured latency.
	for _, candidate := range unknown {
		ordered = append(ordered, candidate.URL)
	}
	for len(known) > 0 {
		best := 0
		bestLatency := normalizedCandidateLatency(known[0])
		for i := 1; i < len(known); i++ {
			latency := normalizedCandidateLatency(known[i])
			if latency < bestLatency {
				best = i
				bestLatency = latency
			}
		}
		ordered = append(ordered, known[best].URL)
		known = append(known[:best], known[best+1:]...)
	}
	return ordered
}

func RuntimeURLState(channel *model.Channel) []RuntimeURLStatus {
	if channel == nil {
		return nil
	}
	return globalRuntimeURLSelector.Snapshot(channel.ID, channel.BaseUrls)
}

func (s *runtimeURLSelector) Snapshot(channelID int, baseURLs []model.BaseUrl) []RuntimeURLStatus {
	urls := compactBaseURLs(baseURLs)
	if len(urls) == 0 {
		return nil
	}
	ordered := s.Candidates(channelID, baseURLs)
	order := make(map[string]int, len(ordered))
	for i, endpoint := range ordered {
		order[endpoint] = i + 1
	}
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	configuredLatency := make(map[string]float64, len(baseURLs))
	for _, baseURL := range baseURLs {
		if baseURL.URL != "" && baseURL.Delay > 0 {
			configuredLatency[baseURL.URL] = float64(baseURL.Delay)
		}
	}
	result := make([]RuntimeURLStatus, 0, len(urls))
	for _, endpoint := range urls {
		key := runtimeURLKey{ChannelID: channelID, URL: endpoint}
		status := RuntimeURLStatus{URL: safeRuntimeURL(endpoint), Rank: order[endpoint]}
		if latency, ok := s.latencies[key]; ok && !runtimeURLStateExpired(latency.LastSeen, now, s.stateTTL) {
			status.Known = true
			status.LatencyMS = max(1, int(math.Round(latency.ValueMS)))
			status.SelectionReason = "measured_latency"
		} else if configured := configuredLatency[endpoint]; configured > 0 {
			status.Known = true
			status.LatencyMS = max(1, int(math.Round(configured)))
			status.SelectionReason = "configured_delay"
		} else {
			status.SelectionReason = "unmeasured"
		}
		if cooldown, ok := s.cooldowns[key]; ok && !runtimeURLStateExpired(cooldown.LastTouched, now, s.stateTTL) && now.Before(cooldown.Until) {
			status.Cooled = true
			status.CooldownRemainingSeconds = max(1, int(math.Ceil(cooldown.Until.Sub(now).Seconds())))
		}
		result = append(result, status)
	}
	return result
}

func safeRuntimeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (s *runtimeURLSelector) RecordSuccess(channelID int, baseURL string, duration time.Duration) {
	if channelID <= 0 || baseURL == "" {
		return
	}
	ms := normalizeRuntimeLatencyMS(duration)
	key := runtimeURLKey{ChannelID: channelID, URL: baseURL}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareWriteLocked(now)

	if current, ok := s.latencies[key]; ok && !runtimeURLStateExpired(current.LastSeen, now, s.stateTTL) {
		current.ValueMS = s.alpha*ms + (1-s.alpha)*current.ValueMS
		current.LastSeen = now
		s.latencies[key] = current
	} else if s.makeLatencyRoomLocked(key, now) {
		s.latencies[key] = runtimeURLLatency{ValueMS: ms, LastSeen: now}
	}
	delete(s.cooldowns, key)
}

func (s *runtimeURLSelector) RecordFailure(channelID int, baseURL string) {
	if channelID <= 0 || baseURL == "" {
		return
	}
	key := runtimeURLKey{ChannelID: channelID, URL: baseURL}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareWriteLocked(now)

	cooldown := s.cooldowns[key]
	if runtimeURLStateExpired(cooldown.LastTouched, now, s.stateTTL) {
		cooldown = runtimeURLCooldown{}
	}
	cooldown.ConsecutiveFails++
	duration := s.cooldownBase * time.Duration(1<<min(cooldown.ConsecutiveFails-1, 20))
	if duration > s.cooldownMax {
		duration = s.cooldownMax
	}
	cooldown.Until = now.Add(duration)
	cooldown.LastTouched = now
	if s.makeCooldownRoomLocked(key, now) {
		s.cooldowns[key] = cooldown
	}
}

func (s *runtimeURLSelector) InvalidateChannel(channelID int) {
	if channelID <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key := range s.latencies {
		if key.ChannelID == channelID {
			delete(s.latencies, key)
		}
	}
	for key := range s.cooldowns {
		if key.ChannelID == channelID {
			delete(s.cooldowns, key)
		}
	}
}

func (s *runtimeURLSelector) InvalidateAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.latencies)
	clear(s.cooldowns)
	s.writeCount = 0
	s.mu.Unlock()
}

func (s *runtimeURLSelector) prepareWriteLocked(now time.Time) {
	s.writeCount++
	if s.sweepEvery > 0 && s.writeCount%s.sweepEvery == 0 {
		s.sweepExpiredLocked(now)
	}
}

func (s *runtimeURLSelector) makeLatencyRoomLocked(key runtimeURLKey, now time.Time) bool {
	if s.maxEntries <= 0 {
		return false
	}
	if _, exists := s.latencies[key]; exists {
		return true
	}
	if len(s.latencies) >= s.maxEntries {
		s.sweepExpiredLocked(now)
	}
	for len(s.latencies) >= s.maxEntries {
		s.evictOldestLatencyLocked()
	}
	return true
}

func (s *runtimeURLSelector) makeCooldownRoomLocked(key runtimeURLKey, now time.Time) bool {
	if s.maxEntries <= 0 {
		return false
	}
	if _, exists := s.cooldowns[key]; exists {
		return true
	}
	if len(s.cooldowns) >= s.maxEntries {
		s.sweepExpiredLocked(now)
	}
	for len(s.cooldowns) >= s.maxEntries {
		s.evictOldestCooldownLocked()
	}
	return true
}

func (s *runtimeURLSelector) sweepExpiredLocked(now time.Time) {
	for key, latency := range s.latencies {
		if runtimeURLStateExpired(latency.LastSeen, now, s.stateTTL) {
			delete(s.latencies, key)
		}
	}
	for key, cooldown := range s.cooldowns {
		if runtimeURLStateExpired(cooldown.LastTouched, now, s.stateTTL) {
			delete(s.cooldowns, key)
		}
	}
}

func (s *runtimeURLSelector) evictOldestLatencyLocked() {
	var oldestKey runtimeURLKey
	var oldest time.Time
	found := false
	for key, latency := range s.latencies {
		if !found || latency.LastSeen.Before(oldest) {
			oldestKey = key
			oldest = latency.LastSeen
			found = true
		}
	}
	if found {
		delete(s.latencies, oldestKey)
	}
}

func (s *runtimeURLSelector) evictOldestCooldownLocked() {
	var oldestKey runtimeURLKey
	var oldest time.Time
	found := false
	for key, cooldown := range s.cooldowns {
		if !found || cooldown.LastTouched.Before(oldest) {
			oldestKey = key
			oldest = cooldown.LastTouched
			found = true
		}
	}
	if found {
		delete(s.cooldowns, oldestKey)
	}
}

func runtimeURLStateExpired(lastTouched, now time.Time, ttl time.Duration) bool {
	if lastTouched.IsZero() {
		return true
	}
	return ttl > 0 && !now.Before(lastTouched.Add(ttl))
}

func compactBaseURLs(baseURLs []model.BaseUrl) []string {
	urls := make([]string, 0, len(baseURLs))
	seen := make(map[string]struct{}, len(baseURLs))
	for _, baseURL := range baseURLs {
		if baseURL.URL == "" {
			continue
		}
		if _, ok := seen[baseURL.URL]; ok {
			continue
		}
		seen[baseURL.URL] = struct{}{}
		urls = append(urls, baseURL.URL)
	}
	return urls
}

func earliestCooldownCandidates(candidates []runtimeURLCandidate) []runtimeURLCandidate {
	if len(candidates) <= 1 {
		return candidates
	}
	earliest := candidates[0].Until
	for _, candidate := range candidates[1:] {
		if candidate.Until.Before(earliest) {
			earliest = candidate.Until
		}
	}
	result := make([]runtimeURLCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Until.Equal(earliest) {
			result = append(result, candidate)
		}
	}
	return result
}

func normalizedCandidateLatency(candidate runtimeURLCandidate) float64 {
	latency := candidate.Latency
	if latency <= 0 || math.IsNaN(latency) || math.IsInf(latency, 0) {
		latency = 0.1
	}
	return latency
}

func normalizeRuntimeLatencyMS(duration time.Duration) float64 {
	ms := float64(duration) / float64(time.Millisecond)
	if ms <= 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return 0.1
	}
	return ms
}
