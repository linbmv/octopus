package relay

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const maxFailureObservationBodyBytes = 8192

// UpstreamFailureObservation is an in-process classification input. It never
// contains a channel key or a client request body. Response content is bounded
// and is consumed synchronously by the registered observer; self-healing must
// persist only the resulting bounded classification fields.
type UpstreamFailureObservation struct {
	ChannelID    int
	ChannelKeyID int
	Model        string
	Endpoint     string
	HTTPStatus   int
	Headers      http.Header
	ResponseBody []byte
	// ErrorLevel carries relay's own classification of this attempt ("key",
	// "channel", "client"). Key-level failures heal by rotating to the next key
	// within the same request, so observers must not count them as channel
	// evidence even when their own re-classification would disagree.
	ErrorLevel     string
	TransportError error
	ObservedAt     time.Time
}

type upstreamFailureObserverHolder struct {
	observe func(UpstreamFailureObservation)
}

var upstreamFailureObserver atomic.Pointer[upstreamFailureObserverHolder]

// RegisterUpstreamFailureObserver installs the single process-wide consumer
// and returns an idempotent unregister function. A stale unregister never
// clears a newer observer installed during a restart.
func RegisterUpstreamFailureObserver(observer func(UpstreamFailureObservation)) func() {
	if observer == nil {
		return func() {}
	}
	holder := &upstreamFailureObserverHolder{observe: observer}
	previous := upstreamFailureObserver.Swap(holder)
	return func() {
		upstreamFailureObserver.CompareAndSwap(holder, previous)
	}
}

func observeUpstreamFailure(observation UpstreamFailureObservation) {
	holder := upstreamFailureObserver.Load()
	if holder == nil || holder.observe == nil {
		return
	}
	observation.Model = strings.TrimSpace(observation.Model)
	observation.Endpoint = strings.TrimSpace(observation.Endpoint)
	observation.Headers = boundedFailureHeaders(observation.Headers)
	if len(observation.ResponseBody) > maxFailureObservationBodyBytes {
		observation.ResponseBody = observation.ResponseBody[:maxFailureObservationBodyBytes]
	}
	observation.ResponseBody = append([]byte(nil), observation.ResponseBody...)
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	holder.observe(observation)
}

func boundedFailureHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	result := make(http.Header)
	for _, name := range []string{"Content-Type", "Retry-After", "X-RateLimit-Scope"} {
		for _, value := range headers.Values(name) {
			value = strings.TrimSpace(value)
			if len(value) > 256 {
				value = value[:256]
			}
			if value != "" {
				result.Add(name, value)
			}
		}
	}
	return result
}
