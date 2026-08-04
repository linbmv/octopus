package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/health"
	"github.com/bestruirui/octopus/internal/routingstate"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestRoutingChangeGuardCancelsOnlyBeforeStop(t *testing.T) {
	first := routingstate.Current()
	ctx, _, release := newRoutingChangeGuard(context.Background(), first.Changed)
	routingstate.Notify()
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), errRoutingConfigChanged) {
			t.Fatalf("guard cause = %v, want routing change", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("routing change did not cancel pre-response guard")
	}
	release()

	second := routingstate.Current()
	ctx, stop, release := newRoutingChangeGuard(context.Background(), second.Changed)
	stop()
	routingstate.Notify()
	select {
	case <-ctx.Done():
		t.Fatalf("stopped routing guard was canceled: %v", context.Cause(ctx))
	case <-time.After(25 * time.Millisecond):
	}
	release()
}

func TestRelayRunReloadsCandidatesAfterRoutingChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	group := dbmodel.Group{
		Name: "m",
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{
			ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m",
		}},
	}
	iter := balancer.NewIterator(group, 1, "m")
	run := &relayRun{
		c:               ginCtx,
		internalRequest: &llm.Request{Model: "m"},
		metrics: &RelayMetrics{
			StartTime: time.Now(), APIKeyID: 1, RequestModel: "m", ActualModel: "m",
		},
		group:           group,
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory:     []*balancer.Iterator{iter},
		routingSnapshot: routingstate.Current(),
	}
	run.attachIteratorTimeline(iter)

	run.reloadRoutingFunc = func() error {
		updated := dbmodel.Group{
			Name: "m",
			Mode: dbmodel.GroupModeFailover,
			Items: []dbmodel.GroupItem{{
				ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m",
			}},
		}
		updatedIter := balancer.NewIterator(updated, 1, "m")
		run.group = updated
		run.iter = updatedIter
		run.iterStack = []*relayIteratorFrame{{group: updated, iter: updatedIter}}
		run.iterHistory = append(run.iterHistory, updatedIter)
		run.attachIteratorTimeline(updatedIter)
		return nil
	}

	started := make(chan struct{})
	attempted := make([]int, 0, 2)
	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		return &relayAttempt{relayRun: run, channel: &dbmodel.Channel{ID: item.ChannelID}}, nil
	}
	run.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		attempted = append(attempted, attempt.channel.ID)
		if attempt.channel.ID == 10 {
			close(started)
			<-run.routingSnapshot.Changed
			return false, errRoutingConfigChanged
		}
		return false, nil
	}
	go func() {
		<-started
		routingstate.Notify()
	}()

	run.run()
	if len(attempted) != 2 || attempted[0] != 10 || attempted[1] != 11 {
		t.Fatalf("attempted channels = %v, want [10 11]", attempted)
	}
	if run.routingRefreshes != 1 {
		t.Fatalf("routing refreshes = %d, want 1", run.routingRefreshes)
	}
}

func TestInvalidateChannelRuntimePenaltiesClearsRateLimitAndHealth(t *testing.T) {
	previousManager := healthManager
	healthManager = health.NewHealthManager(health.DefaultHealthConfig())
	t.Cleanup(func() { healthManager = previousManager })

	const channelID = 9091
	healthManager.RecordSuccess(channelID, 1, "m", time.Second)
	recordChannelRateLimit(channelID, time.Minute)
	if channelRateLimitRemaining(channelID) <= 0 {
		t.Fatal("rate-limit precondition was not established")
	}

	InvalidateChannelRuntimePenalties(channelID, "")
	if channelRateLimitRemaining(channelID) != 0 {
		t.Fatal("channel rate-limit state remained after invalidation")
	}
	for key := range healthManager.GetAllStates() {
		if key.ChannelID == channelID {
			t.Fatalf("health state remained after invalidation: %+v", key)
		}
	}
}
