package relay

import (
	"errors"
	"time"

	"github.com/bestruirui/octopus/internal/relay/balancer"
)

var (
	errUpstreamAttemptBudget  = errors.New("upstream attempt budget exhausted")
	errStreamFirstEventBudget = errors.New("stream first event budget exhausted")
)

const hardMaxInitialResponseTimeoutSeconds = 120

// boundedInitialResponseTimeoutSeconds applies the operator-configured hard
// ceiling to a phase-specific timeout. Zero phase values no longer disable the
// safety boundary: waiting without any response must always remain bounded.
func boundedInitialResponseTimeoutSeconds(configuredSeconds, ceilingSeconds int) int {
	if ceilingSeconds <= 0 || ceilingSeconds > hardMaxInitialResponseTimeoutSeconds {
		ceilingSeconds = hardMaxInitialResponseTimeoutSeconds
	}
	if configuredSeconds <= 0 || configuredSeconds > ceilingSeconds {
		return ceilingSeconds
	}
	return configuredSeconds
}

func (r *relayRun) reserveUpstreamAttempt() bool {
	if r == nil || r.maxUpstreamAttempts <= 0 {
		return true
	}
	if r.upstreamAttempts >= r.maxUpstreamAttempts {
		return false
	}
	r.upstreamAttempts++
	return true
}

func (r *relayRun) remainingStreamFirstEventBudget() time.Duration {
	if r == nil || r.streamFirstEventBudget <= 0 {
		return 0
	}
	remaining := r.streamFirstEventBudget - r.streamFirstEventSpent
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (r *relayRun) recordStreamFirstEventWait(span *balancer.AttemptSpan, err error) {
	if r == nil || r.streamFirstEventBudget <= 0 || r.internalRequest == nil ||
		r.internalRequest.Stream == nil || !*r.internalRequest.Stream || span == nil {
		return
	}
	if r.c != nil && r.c.Request != nil && isRequestContextCanceled(r.c.Request.Context(), err) {
		return
	}
	wait, hasFirstToken := span.FirstTokenDuration()
	if !hasFirstToken {
		wait = span.Duration()
	}
	if wait <= 0 {
		return
	}
	r.streamFirstEventSpent += wait
	if r.streamFirstEventSpent > r.streamFirstEventBudget {
		r.streamFirstEventSpent = r.streamFirstEventBudget
	}
}
