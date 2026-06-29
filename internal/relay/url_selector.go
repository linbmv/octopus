package relay

import (
	"math"
	"math/rand/v2"
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
}

type runtimeURLCandidate struct {
	URL     string
	Latency float64
	Known   bool
	Cooled  bool
	Until   time.Time
}

type runtimeURLSelector struct {
	mu           sync.RWMutex
	latencies    map[runtimeURLKey]runtimeURLLatency
	cooldowns    map[runtimeURLKey]runtimeURLCooldown
	alpha        float64
	cooldownBase time.Duration
	cooldownMax  time.Duration
	now          func() time.Time
}

func newRuntimeURLSelector() *runtimeURLSelector {
	return &runtimeURLSelector{
		latencies:    make(map[runtimeURLKey]runtimeURLLatency),
		cooldowns:    make(map[runtimeURLKey]runtimeURLCooldown),
		alpha:        0.3,
		cooldownBase: 2 * time.Minute,
		cooldownMax:  30 * time.Minute,
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

func recordRuntimeURLSuccess(channelID int, baseURL string, duration time.Duration) {
	globalRuntimeURLSelector.RecordSuccess(channelID, baseURL, duration)
}

func recordRuntimeURLFailure(channelID int, baseURL string) {
	globalRuntimeURLSelector.RecordFailure(channelID, baseURL)
}

func (s *runtimeURLSelector) Select(channelID int, baseURLs []model.BaseUrl) string {
	urls := compactBaseURLs(baseURLs)
	if len(urls) == 0 {
		return ""
	}
	if len(urls) == 1 {
		return urls[0]
	}

	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	candidates := make([]runtimeURLCandidate, 0, len(urls))
	for _, url := range urls {
		key := runtimeURLKey{ChannelID: channelID, URL: url}
		item := runtimeURLCandidate{URL: url}
		if latency, ok := s.latencies[key]; ok {
			item.Latency = latency.ValueMS
			item.Known = true
		}
		if cooldown, ok := s.cooldowns[key]; ok && now.Before(cooldown.Until) {
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
		return urls[0]
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
	if len(unknown) > 0 {
		return unknown[rand.IntN(len(unknown))].URL
	}
	return weightedLatencyPick(known)
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

	if current, ok := s.latencies[key]; ok {
		current.ValueMS = s.alpha*ms + (1-s.alpha)*current.ValueMS
		current.LastSeen = now
		s.latencies[key] = current
	} else {
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

	cooldown := s.cooldowns[key]
	cooldown.ConsecutiveFails++
	duration := s.cooldownBase * time.Duration(1<<min(cooldown.ConsecutiveFails-1, 20))
	if duration > s.cooldownMax {
		duration = s.cooldownMax
	}
	cooldown.Until = now.Add(duration)
	s.cooldowns[key] = cooldown
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

func weightedLatencyPick(candidates []runtimeURLCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	bestLatency := normalizedCandidateLatency(best)
	for _, candidate := range candidates[1:] {
		latency := normalizedCandidateLatency(candidate)
		if latency < bestLatency {
			best = candidate
			bestLatency = latency
		}
	}
	return best.URL
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
