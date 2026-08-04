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

func TestRoutingRefreshSkipsUnchangedInterruptedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	oldGroup := dbmodel.Group{
		Name: "m", Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m"}},
	}
	oldIter := balancer.NewIterator(oldGroup, 1, "m")
	run := &relayRun{
		c: ginCtx, internalRequest: &llm.Request{Model: "m"},
		metrics: &RelayMetrics{StartTime: time.Now(), APIKeyID: 1, RequestModel: "m", ActualModel: "m"},
		group:   oldGroup, iter: oldIter,
		iterStack:   []*relayIteratorFrame{{group: oldGroup, iter: oldIter}},
		iterHistory: []*balancer.Iterator{oldIter}, routingSnapshot: routingstate.Current(),
		failoverState: newRequestFailoverState(),
	}
	run.attachIteratorTimeline(oldIter)

	run.reloadRoutingFunc = func() error {
		updated := dbmodel.Group{
			Name: "m", Mode: dbmodel.GroupModeFailover,
			Items: []dbmodel.GroupItem{
				{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
				{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
			},
		}
		updatedIter := balancer.NewIterator(updated, 1, "m")
		run.group = updated
		run.iter = updatedIter
		run.iterStack = []*relayIteratorFrame{{group: updated, iter: updatedIter}}
		run.iterHistory = append(run.iterHistory, updatedIter)
		run.attachIteratorTimeline(updatedIter)
		return nil
	}
	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		channel := &dbmodel.Channel{ID: item.ChannelID, ConfigVersion: 1, Type: llm.APIFormatOpenAIResponse}
		return &relayAttempt{
			relayRun: run, channel: channel, groupItem: item,
			usedKey: dbmodel.ChannelKey{ID: item.ChannelID, ChannelKey: "test"},
			baseURL: "https://provider.example/v1",
		}, nil
	}

	attempted := make([]int, 0, 2)
	run.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		attempted = append(attempted, attempt.channel.ID)
		if attempt.channel.ID == 10 {
			routingstate.Notify()
			return false, errRoutingConfigChanged
		}
		return false, nil
	}

	run.run()
	if len(attempted) != 2 || attempted[0] != 10 || attempted[1] != 11 {
		t.Fatalf("attempted channels = %v, want [10 11] without retrying unchanged channel 10", attempted)
	}
}

func TestRoutingRefreshAllowsModifiedInterruptedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	item := dbmodel.GroupItem{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m"}
	group := dbmodel.Group{Name: "m", Mode: dbmodel.GroupModeFailover, Items: []dbmodel.GroupItem{item}}
	iter := balancer.NewIterator(group, 1, "m")
	run := &relayRun{
		c: ginCtx, internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{StartTime: time.Now(), APIKeyID: 1, RequestModel: "m", ActualModel: "m"},
		group:           group,
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory:     []*balancer.Iterator{iter},
		routingSnapshot: routingstate.Current(),
		failoverState:   newRequestFailoverState(),
	}
	run.attachIteratorTimeline(iter)

	configVersion := 1
	run.reloadRoutingFunc = func() error {
		configVersion = 2
		updatedIter := balancer.NewIterator(group, 1, "m")
		run.iter = updatedIter
		run.iterStack = []*relayIteratorFrame{{group: group, iter: updatedIter}}
		run.iterHistory = append(run.iterHistory, updatedIter)
		run.attachIteratorTimeline(updatedIter)
		return nil
	}
	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		return &relayAttempt{
			relayRun: run,
			channel: &dbmodel.Channel{
				ID: item.ChannelID, ConfigVersion: configVersion, Type: llm.APIFormatOpenAIResponse,
			},
			groupItem: item,
			usedKey:   dbmodel.ChannelKey{ID: 1, ChannelKey: "test"},
			baseURL:   "https://provider.example/v1",
		}, nil
	}

	attemptedVersions := make([]int, 0, 2)
	run.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		attemptedVersions = append(attemptedVersions, attempt.channel.ConfigVersion)
		if len(attemptedVersions) == 1 {
			routingstate.Notify()
			return false, errRoutingConfigChanged
		}
		return false, nil
	}

	run.run()
	if len(attemptedVersions) != 2 || attemptedVersions[0] != 1 || attemptedVersions[1] != 2 {
		t.Fatalf("attempted config versions = %v, want [1 2]", attemptedVersions)
	}
	if run.routingRefreshes != 1 {
		t.Fatalf("routing refreshes = %d, want 1", run.routingRefreshes)
	}
}

func TestPrepareAttemptSkipsBlockedCandidateWithMissingChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	item := dbmodel.GroupItem{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m"}
	group := dbmodel.Group{Name: "m", Mode: dbmodel.GroupModeFailover, Items: []dbmodel.GroupItem{item}}
	iter := balancer.NewIterator(group, 1, "m")
	run := &relayRun{
		c: ginCtx, internalRequest: &llm.Request{Model: "m"}, metrics: &RelayMetrics{},
		group: group, iter: iter,
		iterStack:   []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory: []*balancer.Iterator{iter}, failoverState: newRequestFailoverState(),
	}
	run.failoverState.exhaust(newRequestCandidateID(nil, dbmodel.ChannelKey{}, "m", ""), requestFailureCandidate)
	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		return &relayAttempt{relayRun: run, groupItem: item}, nil
	}

	attempt, err := run.prepareAttempt()
	if err != nil || attempt != nil {
		t.Fatalf("prepareAttempt() = (%#v, %v), want exhausted malformed candidate skipped", attempt, err)
	}
}

func TestInvalidateChannelRuntimePenaltiesClearsRateLimitAndHealth(t *testing.T) {
	previousManager := healthManager
	healthManager = health.NewHealthManager(health.DefaultHealthConfig())
	t.Cleanup(func() { healthManager = previousManager })

	const channelID = 9091
	healthManager.RecordSuccess(channelID, 1, "m", time.Second)
	recordChannelRateLimit(channelID, time.Minute)
	slowKey := newSlowRecoveryKey(
		&dbmodel.Channel{ID: channelID, ConfigVersion: 1, Type: llm.APIFormatOpenAIResponse},
		dbmodel.ChannelKey{ID: 1, ChannelKey: "test"}, "m", "https://slow.example/v1",
	)
	globalSlowRecovery.recordTimeout(slowKey, 30*time.Second)
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
	globalSlowRecovery.mu.Lock()
	_, slowExists := globalSlowRecovery.entries[slowKey]
	globalSlowRecovery.mu.Unlock()
	if slowExists {
		t.Fatal("passive slow-recovery state remained after channel invalidation")
	}
}

func TestInvalidateAllRuntimeStateClearsRateLimitURLAndHealth(t *testing.T) {
	globalChannelRPMLimiter.clear()
	t.Cleanup(globalChannelRPMLimiter.clear)
	previousManager := healthManager
	healthManager = health.NewHealthManager(health.DefaultHealthConfig())
	t.Cleanup(func() { healthManager = previousManager })

	const channelID = 9092
	healthManager.RecordSuccess(channelID, 1, "m", time.Second)
	recordChannelRateLimit(channelID, time.Minute)
	recordRuntimeURLFailure(channelID, "https://restore.example/v1")
	globalSlowRecovery.recordTimeout(newSlowRecoveryKey(
		&dbmodel.Channel{ID: channelID, ConfigVersion: 1, Type: llm.APIFormatOpenAIResponse},
		dbmodel.ChannelKey{ID: 1, ChannelKey: "test"}, "m", "https://restore.example/v1",
	), 30*time.Second)
	if first := globalChannelRPMLimiter.reserve(channelID, 1); !first.Allowed {
		t.Fatal("RPM limiter precondition rejected the first request")
	}
	if second := globalChannelRPMLimiter.reserve(channelID, 1); second.Allowed {
		t.Fatal("RPM limiter precondition did not retain the old request window")
	}

	InvalidateAllRuntimeState()
	if channelRateLimitRemaining(channelID) != 0 {
		t.Fatal("bulk invalidation retained channel rate limit")
	}
	if len(healthManager.GetAllStates()) != 0 {
		t.Fatal("bulk invalidation retained health state")
	}
	globalSlowRecovery.mu.Lock()
	slowEntries := len(globalSlowRecovery.entries)
	globalSlowRecovery.mu.Unlock()
	if slowEntries != 0 {
		t.Fatalf("bulk invalidation retained %d passive slow-recovery entries", slowEntries)
	}
	globalRuntimeURLSelector.mu.Lock()
	latencies := len(globalRuntimeURLSelector.latencies)
	cooldowns := len(globalRuntimeURLSelector.cooldowns)
	globalRuntimeURLSelector.mu.Unlock()
	if latencies != 0 || cooldowns != 0 {
		t.Fatalf("bulk invalidation retained URL state: latency=%d cooldown=%d", latencies, cooldowns)
	}
	if reservation := globalChannelRPMLimiter.reserve(channelID, 1); !reservation.Allowed {
		t.Fatal("bulk invalidation retained local RPM request history")
	}
}
