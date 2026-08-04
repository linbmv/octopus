package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/routingstate"
	"github.com/bestruirui/octopus/internal/utils/log"
)

var errRoutingConfigChanged = errors.New("routing configuration changed")

const maxRoutingRefreshesPerRequest = 8

func (r *relayRun) routingChanged() bool {
	if r == nil || r.routingSnapshot.Changed == nil {
		return false
	}
	select {
	case <-r.routingSnapshot.Changed:
		return true
	default:
		return false
	}
}

func (r *relayRun) refreshRouting() error {
	if r == nil || r.c == nil || r.c.Request == nil || r.internalRequest == nil || r.metrics == nil {
		return newTerminalRelayError(http.StatusServiceUnavailable, errors.New("routing state unavailable"))
	}
	if r.routingRefreshes >= maxRoutingRefreshesPerRequest {
		return newTerminalRelayError(
			http.StatusServiceUnavailable,
			fmt.Errorf("routing changed more than %d times during one request", maxRoutingRefreshesPerRequest),
		)
	}

	snapshot := routingstate.Current()
	r.routingRefreshes++
	if r.reloadRoutingFunc != nil {
		if err := r.reloadRoutingFunc(); err != nil {
			return err
		}
		r.routingSnapshot = snapshot
		return nil
	}

	requestModel := r.metrics.RequestModel
	group, err := op.GroupGetEnabledTree(requestModel, r.c.Request.Context())
	if err != nil {
		return newTerminalRelayError(http.StatusServiceUnavailable, fmt.Errorf("reload routing group: %w", err))
	}

	// Candidate resolution mutates internalRequest.Model to the selected upstream
	// model. Restore the inbound model before rebuilding the root iterator.
	r.internalRequest.Model = requestModel
	selectedBaseURLs := make(map[int]string)
	iter := newRelayIteratorWithSessionAndBaseURLs(
		group,
		r.metrics.APIKeyID,
		r.internalRequest,
		r.c.Request.Context(),
		selectedBaseURLs,
		r.sessionID,
	)
	if iter.Len() == 0 {
		return newTerminalRelayError(http.StatusServiceUnavailable, errors.New("no available channel after routing change"))
	}

	r.group = group
	r.iter = iter
	r.iterStack = []*relayIteratorFrame{{group: group, iter: iter, depth: 0}}
	r.iterHistory = append(r.iterHistory, iter)
	r.selectedBaseURLs = selectedBaseURLs
	r.routingSnapshot = snapshot
	r.attachIteratorTimeline(iter)
	log.Infof("routing configuration refreshed: revision=%d refresh=%d candidates=%d",
		snapshot.Revision, r.routingRefreshes, iter.Len())
	return nil
}

// newRoutingChangeGuard cancels only the pre-response upstream phase. Calling
// stop after response headers (non-streaming) or the first non-empty event
// (streaming) detaches the watcher so an established response is never replayed.
func newRoutingChangeGuard(parent context.Context, changed <-chan struct{}) (
	ctx context.Context,
	stop func(),
	release func(),
) {
	if parent == nil {
		parent = context.Background()
	}
	if changed == nil {
		return parent, func() {}, func() {}
	}

	guarded, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	var settled atomic.Bool
	var doneOnce sync.Once
	go func() {
		select {
		case <-changed:
			if settled.CompareAndSwap(false, true) {
				cancel(errRoutingConfigChanged)
			}
		case <-done:
		case <-parent.Done():
		}
	}()

	stop = func() {
		if settled.CompareAndSwap(false, true) {
			doneOnce.Do(func() { close(done) })
		}
	}
	release = func() {
		stop()
		cancel(nil)
	}
	return guarded, stop, release
}

func markRoutingRefreshAttempt(span *balancer.AttemptSpan, channel *dbmodel.Channel, key dbmodel.ChannelKey, modelName, baseURL string) {
	if channel != nil {
		balancer.RecordProbeAbort(channel.ID, key.ID, modelName)
	}
	if span == nil {
		return
	}
	span.SetRoutingMetadata(baseURL, "routing_refresh", false, "configuration_changed")
	span.End(dbmodel.AttemptSkipped, errRoutingConfigChanged.Error())
}
