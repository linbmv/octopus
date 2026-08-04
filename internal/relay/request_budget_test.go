package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestRelayRunUpstreamAttemptBudgetIsExact(t *testing.T) {
	run := &relayRun{maxUpstreamAttempts: 2}
	if !run.reserveUpstreamAttempt() {
		t.Fatal("first upstream attempt should be allowed")
	}
	if !run.reserveUpstreamAttempt() {
		t.Fatal("second upstream attempt should be allowed")
	}
	if run.reserveUpstreamAttempt() {
		t.Fatal("third upstream attempt should be rejected")
	}
	if run.upstreamAttempts != 2 {
		t.Fatalf("upstream attempt count = %d, want 2", run.upstreamAttempts)
	}
	unlimited := &relayRun{}
	for range 100 {
		if !unlimited.reserveUpstreamAttempt() {
			t.Fatal("zero max attempts should disable the limit")
		}
	}
}

func TestBoundedInitialResponseTimeoutCannotBeDisabledOrRaisedAboveHardLimit(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		ceiling    int
		want       int
	}{
		{name: "phase below ceiling", configured: 30, ceiling: 90, want: 30},
		{name: "phase above ceiling", configured: 600, ceiling: 90, want: 90},
		{name: "disabled phase uses ceiling", configured: 0, ceiling: 90, want: 90},
		{name: "invalid ceiling uses hard max", configured: 600, ceiling: 0, want: hardMaxInitialResponseTimeoutSeconds},
		{name: "oversized ceiling uses hard max", configured: 600, ceiling: 600, want: hardMaxInitialResponseTimeoutSeconds},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedInitialResponseTimeoutSeconds(test.configured, test.ceiling); got != test.want {
				t.Fatalf("bounded timeout = %d, want %d", got, test.want)
			}
		})
	}
}

func TestStreamIdleTimeoutCannotBeDisabledOrRaisedAboveHardWaitLimit(t *testing.T) {
	config := conf.Default().Relay
	config.InitialResponseTimeoutSeconds = 90

	for _, test := range []struct {
		name       string
		configured int
		want       time.Duration
	}{
		{name: "shorter configured idle", configured: 30, want: 30 * time.Second},
		{name: "oversized configured idle", configured: 600, want: 90 * time.Second},
		{name: "disabled configured idle", configured: 0, want: 90 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			config.StreamIdleTimeoutSeconds = test.configured
			if got := boundedStreamIdleTimeout(config); got != test.want {
				t.Fatalf("bounded stream idle timeout = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRelayRunStreamFirstEventBudgetTracksRemainingTime(t *testing.T) {
	run := &relayRun{
		streamFirstEventBudget: 90 * time.Second,
		streamFirstEventSpent:  35 * time.Second,
	}
	if got := run.remainingStreamFirstEventBudget(); got != 55*time.Second {
		t.Fatalf("remaining budget = %v, want 55s", got)
	}
	run.streamFirstEventSpent = 100 * time.Second
	if got := run.remainingStreamFirstEventBudget(); got != 0 {
		t.Fatalf("overdrawn remaining budget = %v, want 0", got)
	}
	if got := (&relayRun{}).remainingStreamFirstEventBudget(); got != 0 {
		t.Fatalf("disabled remaining budget = %v, want 0", got)
	}
}

func TestFirstTokenBudgetTimeoutIsDistinctFromAdaptiveTimeout(t *testing.T) {
	budgetErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutBudget}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if !isFirstTokenBudgetTimeout(budgetErr) {
		t.Fatal("budget timeout was not recognized")
	}
	adaptiveErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutAdaptive}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if isFirstTokenBudgetTimeout(adaptiveErr) {
		t.Fatal("adaptive timeout was classified as request budget exhaustion")
	}
}

func TestStreamFirstEventBudgetCountsSuccessfulFirstTokenWait(t *testing.T) {
	stream := true
	run := &relayRun{
		internalRequest:        &llm.Request{Stream: &stream},
		streamFirstEventBudget: time.Second,
	}
	iter := balancer.NewIterator(model.Group{Items: []model.GroupItem{{ChannelID: 1, ModelName: "m"}}}, 0, "m")
	if !iter.Next() {
		t.Fatal("iterator has no candidate")
	}
	span := iter.StartAttempt(1, 1, "channel", "key")
	span.RecordFirstToken(time.Now().Add(50 * time.Millisecond))
	run.recordStreamFirstEventWait(span, nil)
	if run.streamFirstEventSpent < 40*time.Millisecond || run.streamFirstEventSpent > 200*time.Millisecond {
		t.Fatalf("successful first token wait = %v, want approximately 50ms", run.streamFirstEventSpent)
	}
}

func TestBudgetExhaustionStopsWithoutMarkingUnattemptedCandidatesFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	items := []model.GroupItem{
		{ID: 1, Type: model.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
		{ID: 2, Type: model.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
	}
	group := model.Group{Name: "m", Mode: model.GroupModeFailover, Items: items}
	iter := balancer.NewIterator(group, 1, "m")
	run := &relayRun{
		c: ginCtx, internalRequest: &llm.Request{Model: "m"},
		metrics:       &RelayMetrics{StartTime: time.Now(), APIKeyID: 1, RequestModel: "m", ActualModel: "m"},
		group:         group,
		iter:          iter,
		iterStack:     []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory:   []*balancer.Iterator{iter},
		failoverState: newRequestFailoverState(),
	}
	run.attachIteratorTimeline(iter)
	run.resolveGroupItemFunc = func(item model.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		return &relayAttempt{
			relayRun:  run,
			channel:   &model.Channel{ID: item.ChannelID, ConfigVersion: 1, Type: llm.APIFormatOpenAIResponse},
			groupItem: item,
			usedKey:   model.ChannelKey{ID: item.ChannelID, ChannelKey: "not-a-real-key"},
			baseURL:   "https://provider.example/v1",
		}, nil
	}

	attempted := make([]int, 0, len(items))
	run.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		attempted = append(attempted, attempt.channel.ID)
		return false, newTerminalRelayError(http.StatusGatewayTimeout, errStreamFirstEventBudget)
	}

	run.run()
	if len(attempted) != 1 || attempted[0] != 10 {
		t.Fatalf("attempted channels = %v, want only the first candidate", attempted)
	}
	unattempted := newRequestCandidateID(
		&model.Channel{ID: 11, ConfigVersion: 1},
		model.ChannelKey{ID: 11},
		"m",
		"https://provider.example/v1",
	)
	if !run.failoverState.allows(unattempted) {
		t.Fatal("budget exhaustion incorrectly marked the unattempted candidate as failed")
	}
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
}
