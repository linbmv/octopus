package relay

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRateLimitCooldown = time.Minute
	maxRateLimitCooldown     = 7 * 24 * time.Hour
	channelRateLimitLimit    = 4096
)

type channelRateLimitState struct {
	mu    sync.Mutex
	until map[int]time.Time
	now   func() time.Time
}

func newChannelRateLimitState() *channelRateLimitState {
	return &channelRateLimitState{until: make(map[int]time.Time), now: time.Now}
}

var globalChannelRateLimits = newChannelRateLimitState()

func retryAfterDuration(headers http.Header, now time.Time, fallback time.Duration) time.Duration {
	value := ""
	if headers != nil {
		value = strings.TrimSpace(headers.Get("Retry-After"))
	}
	if value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			return min(time.Duration(seconds)*time.Second, maxRateLimitCooldown)
		}
		if deadline, err := http.ParseTime(value); err == nil {
			if duration := deadline.Sub(now); duration > 0 {
				return min(duration, maxRateLimitCooldown)
			}
			return 0
		}
	}
	if fallback < 0 {
		return 0
	}
	return min(fallback, maxRateLimitCooldown)
}

func recordChannelRateLimit(channelID int, duration time.Duration) {
	globalChannelRateLimits.record(channelID, duration)
}

func channelRateLimitRemaining(channelID int) time.Duration {
	return globalChannelRateLimits.remaining(channelID)
}

func (s *channelRateLimitState) record(channelID int, duration time.Duration) {
	if s == nil || channelID <= 0 || duration <= 0 {
		return
	}
	now := s.now()
	deadline := now.Add(duration)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, until := range s.until {
		if !until.After(now) {
			delete(s.until, id)
		}
	}
	if len(s.until) >= channelRateLimitLimit {
		var earliestID int
		var earliest time.Time
		for id, until := range s.until {
			if earliestID == 0 || until.Before(earliest) {
				earliestID = id
				earliest = until
			}
		}
		delete(s.until, earliestID)
	}
	if current := s.until[channelID]; deadline.After(current) {
		s.until[channelID] = deadline
	}
}

func (s *channelRateLimitState) remaining(channelID int) time.Duration {
	if s == nil || channelID <= 0 {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline := s.until[channelID]
	if !deadline.After(now) {
		delete(s.until, channelID)
		return 0
	}
	return deadline.Sub(now)
}

func (s *channelRateLimitState) invalidate(channelID int) {
	if s == nil || channelID <= 0 {
		return
	}
	s.mu.Lock()
	delete(s.until, channelID)
	s.mu.Unlock()
}
