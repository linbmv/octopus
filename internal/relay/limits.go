package relay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

type channelRequestLimitError struct {
	message string
}

func (e *channelRequestLimitError) Error() string {
	if e == nil {
		return "channel request limit reached"
	}
	return e.message
}

func isChannelRequestLimitError(err error) bool {
	var target *channelRequestLimitError
	return errors.As(err, &target)
}

type channelRPMReservation struct {
	Allowed    bool
	RetryAfter time.Duration
}

type channelRPMLimiter struct {
	mu       sync.Mutex
	requests map[int][]time.Time
	now      func() time.Time
}

func newChannelRPMLimiter(now func() time.Time) *channelRPMLimiter {
	if now == nil {
		now = time.Now
	}
	return &channelRPMLimiter{
		requests: make(map[int][]time.Time),
		now:      now,
	}
}

func (l *channelRPMLimiter) reserve(channelID int, limit int) channelRPMReservation {
	if l == nil || channelID <= 0 || limit <= 0 {
		return channelRPMReservation{Allowed: true}
	}

	now := l.now()
	cutoff := now.Add(-time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()

	events := l.requests[channelID]
	kept := 0
	for _, ts := range events {
		if ts.After(cutoff) {
			events[kept] = ts
			kept++
		}
	}
	events = events[:kept]

	if len(events) >= limit {
		retryAfter := time.Minute
		if len(events) > 0 {
			retryAfter = events[0].Add(time.Minute).Sub(now)
			if retryAfter < 0 {
				retryAfter = 0
			}
		}
		l.requests[channelID] = events
		return channelRPMReservation{Allowed: false, RetryAfter: retryAfter}
	}

	l.requests[channelID] = append(events, now)
	return channelRPMReservation{Allowed: true}
}

func (l *channelRPMLimiter) clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	clear(l.requests)
	l.mu.Unlock()
}

type channelConcurrencyLimiter struct {
	mu     sync.Mutex
	active map[int]int
}

func newChannelConcurrencyLimiter() *channelConcurrencyLimiter {
	return &channelConcurrencyLimiter{active: make(map[int]int)}
}

func (l *channelConcurrencyLimiter) acquire(channelID int, limit int) (func(), int, int, bool) {
	if l == nil || channelID <= 0 || limit <= 0 {
		return func() {}, 0, 0, true
	}

	l.mu.Lock()
	current := l.active[channelID]
	if current >= limit {
		l.mu.Unlock()
		return nil, current, limit, false
	}
	l.active[channelID] = current + 1
	l.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			current := l.active[channelID]
			if current <= 1 {
				delete(l.active, channelID)
				return
			}
			l.active[channelID] = current - 1
		})
	}
	return release, current + 1, limit, true
}

var globalChannelRPMLimiter = newChannelRPMLimiter(nil)
var globalChannelConcurrencyLimiter = newChannelConcurrencyLimiter()

func reserveChannelLimits(channel *model.Channel) (func(), string, bool) {
	if channel == nil {
		return func() {}, "", true
	}

	release, active, limit, ok := globalChannelConcurrencyLimiter.acquire(channel.ID, channel.MaxConcurrency)
	if !ok {
		return nil, fmt.Sprintf("channel concurrency limit reached: active=%d limit=%d", active, limit), false
	}
	return release, "", true
}

func reserveChannelRPM(channel *model.Channel) (string, bool) {
	if channel == nil {
		return "", true
	}
	reservation := globalChannelRPMLimiter.reserve(channel.ID, channel.RPMLimit)
	if reservation.Allowed {
		return "", true
	}
	return fmt.Sprintf("channel RPM limit reached: retry_after=%ds", int(reservation.RetryAfter.Seconds())), false
}
