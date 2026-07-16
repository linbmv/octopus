package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	serverresp "github.com/bestruirui/octopus/internal/server/resp"
	projectlog "github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecordUsageNilNoop(t *testing.T) {
	m := &RelayMetrics{ActualModel: "x"}
	m.RecordUsage(nil)
	if m.Stats.InputToken != 0 || m.Stats.OutputToken != 0 {
		t.Fatal("nil usage 必须是空操作")
	}
}

func TestRecordUsageRecordsTokensWithoutPrice(t *testing.T) {
	// 价格缓存未命中（无 DB 环境）时，仍应记录 token 用量，成本保持 0。
	m := &RelayMetrics{ActualModel: "model-without-price"}
	m.RecordUsage(&llm.Usage{PromptTokens: 100, CompletionTokens: 40})
	if m.Stats.InputToken != 100 || m.Stats.OutputToken != 40 {
		t.Fatalf("token 未记录: input=%d output=%d", m.Stats.InputToken, m.Stats.OutputToken)
	}
	if m.Stats.InputCost != 0 || m.Stats.OutputCost != 0 {
		t.Fatalf("无价格时成本应为 0: input=%f output=%f", m.Stats.InputCost, m.Stats.OutputCost)
	}
}

func TestFilterRequestForLogStripsRawAndImageBytes(t *testing.T) {
	req := &llm.Request{
		Model:      "m",
		RawRequest: &httpclient.Request{Body: []byte("raw")},
		Image: &llm.ImageRequest{
			Images: [][]byte{[]byte("imgdata")},
			Mask:   []byte("maskdata"),
		},
	}
	got := filterRequestForLog(req)

	if got.RawRequest != nil {
		t.Fatal("RawRequest 应被剥离")
	}
	if got.Image == nil || len(got.Image.Images) != 0 {
		t.Fatal("图片二进制应被清空")
	}
	if len(got.Image.Mask) != 0 {
		t.Fatal("mask 二进制应被清空")
	}
	// 过滤是为日志服务的副本操作，绝不能改动原始请求。
	if req.RawRequest == nil || len(req.Image.Images) != 1 || len(req.Image.Mask) != 8 {
		t.Fatal("原始请求不应被修改")
	}
}

func TestFilterRequestForLogNil(t *testing.T) {
	if filterRequestForLog(nil) != nil {
		t.Fatal("nil 请求应返回 nil")
	}
}

func TestFinalAttemptReturnsSuccess(t *testing.T) {
	attempts := []dbmodel.ChannelAttempt{
		{ChannelID: 1, ChannelName: "a", Status: dbmodel.AttemptFailed},
		{ChannelID: 2, ChannelName: "b", ChannelKeyID: 7, Status: dbmodel.AttemptSuccess},
	}
	id, name, keyID, status := finalAttempt(attempts)
	if id != 2 || name != "b" || keyID != 7 || status != dbmodel.AttemptSuccess {
		t.Fatalf("got id=%d name=%s key=%d status=%s, 期望成功的尝试", id, name, keyID, status)
	}
}

func TestFinalAttemptReturnsLastFailedWhenNoSuccess(t *testing.T) {
	attempts := []dbmodel.ChannelAttempt{
		{ChannelID: 1, ChannelName: "a", ChannelKeyID: 11, Status: dbmodel.AttemptFailed},
		{ChannelID: 2, ChannelName: "b", ChannelKeyID: 22, Status: dbmodel.AttemptFailed},
	}
	id, name, keyID, status := finalAttempt(attempts)
	// 没有成功尝试时，应归因到最后一次失败的通道。
	if id != 2 || name != "b" || keyID != 22 || status != dbmodel.AttemptFailed {
		t.Fatalf("got id=%d name=%s key=%d status=%s, 期望最后一次失败 (2,b,22,failed)", id, name, keyID, status)
	}
}

func TestFinalAttemptReturnsClientCanceledWhenNoSuccess(t *testing.T) {
	attempts := []dbmodel.ChannelAttempt{
		{ChannelID: 1, ChannelName: "a", ChannelKeyID: 11, Status: dbmodel.AttemptClientCancel},
	}
	id, name, keyID, status := finalAttempt(attempts)
	if id != 1 || name != "a" || keyID != 11 || status != dbmodel.AttemptClientCancel {
		t.Fatalf("got id=%d name=%s key=%d status=%s, want client canceled attempt", id, name, keyID, status)
	}
}

func TestFinalAttemptReturnsCircuitBreakWhenNoSuccess(t *testing.T) {
	attempts := []dbmodel.ChannelAttempt{
		{ChannelID: 38, ChannelName: "T1", ChannelKeyID: 71, Status: dbmodel.AttemptCircuitBreak},
	}
	id, name, keyID, status := finalAttempt(attempts)
	if id != 38 || name != "T1" || keyID != 71 || status != dbmodel.AttemptCircuitBreak {
		t.Fatalf("got id=%d name=%s key=%d status=%s, want circuit break attempt", id, name, keyID, status)
	}
}

func TestFinalAttemptEmpty(t *testing.T) {
	id, name, keyID, status := finalAttempt(nil)
	if id != 0 || name != "" || keyID != 0 || status != "" {
		t.Fatalf("空尝试应返回零值, got id=%d name=%q key=%d status=%q", id, name, keyID, status)
	}
}

func TestRelayRunAttemptsRenumbersNestedIteratorAttempts(t *testing.T) {
	parentGroup := dbmodel.Group{Items: []dbmodel.GroupItem{{ModelName: "parent-model"}}}
	childGroup := dbmodel.Group{Items: []dbmodel.GroupItem{{ModelName: "child-model"}}}
	parentIter := balancer.NewIterator(parentGroup, 1, "request-model")
	childIter := balancer.NewIterator(childGroup, 1, "request-model")
	if !parentIter.Next() || !childIter.Next() {
		t.Fatal("测试 iterator 应包含候选")
	}
	parentIter.Skip(1, 0, "parent", "parent skipped")
	childIter.Skip(2, 0, "child", "child skipped")

	r := &relayRun{iterHistory: []*balancer.Iterator{parentIter, childIter}}
	attempts := r.attempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts 数量 = %d, 期望 2", len(attempts))
	}
	if attempts[0].AttemptNum != 1 || attempts[1].AttemptNum != 2 {
		t.Fatalf("attempt num 应全局连续, got %d/%d", attempts[0].AttemptNum, attempts[1].AttemptNum)
	}
	if attempts[0].ChannelID != 1 || attempts[1].ChannelID != 2 {
		t.Fatalf("attempt 顺序被改变: %+v", attempts)
	}
}

func TestPrepareAttemptContinuesAfterSkippedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
		},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
	}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		if item.ChannelID == 10 {
			r.iter.Skip(item.ChannelID, 0, "disabled", "channel disabled")
			return nil, nil
		}
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID, Name: "ok"}}, nil
	}

	attempt, err := r.prepareAttempt()
	if err != nil {
		t.Fatalf("prepareAttempt error = %v", err)
	}
	if attempt == nil || attempt.channel.ID != 11 {
		t.Fatalf("prepareAttempt should continue to channel 11, got %+v", attempt)
	}
	attempts := r.attempts()
	if len(attempts) != 1 || attempts[0].Status != dbmodel.AttemptSkipped || attempts[0].ChannelID != 10 {
		t.Fatalf("skipped attempt not recorded correctly: %+v", attempts)
	}
}

func TestPrepareAttemptContinuesAfterCircuitBreak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 21, ModelName: "gpt-5.5", Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 5, ModelName: "gpt-5.5", Priority: 2},
		},
	}
	iter := balancer.NewIterator(group, 1, "gpt-5.5")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "gpt-5.5"},
		metrics:         &RelayMetrics{},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
	}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		if item.ChannelID == 21 {
			r.iter.SkipFor(item, sticky, item.ChannelID, 38, "Anyrouter_codex", "circuit breaker tripped, remaining cooldown: 48s")
			attempts := r.iter.Attempts()
			attempts[len(attempts)-1].Status = dbmodel.AttemptCircuitBreak
			return nil, nil
		}
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID, Name: "Linuxdo_WONG"}}, nil
	}

	attempt, err := r.prepareAttempt()
	if err != nil {
		t.Fatalf("prepareAttempt error = %v", err)
	}
	if attempt == nil || attempt.channel.ID != 5 {
		t.Fatalf("prepareAttempt should continue to channel 5 after circuit break, got %+v", attempt)
	}
	attempts := r.attempts()
	if len(attempts) != 1 || attempts[0].Status != dbmodel.AttemptCircuitBreak || attempts[0].ChannelID != 21 {
		t.Fatalf("circuit break attempt not recorded correctly: %+v", attempts)
	}
}

func TestPrepareAttemptSkipsNestedGroupBeyondMaxDepth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 99, Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
		},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: op.MaxGroupNestDepth}},
		iterHistory:     []*balancer.Iterator{iter},
	}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID, Name: "ok"}}, nil
	}

	attempt, err := r.prepareAttempt()
	if err != nil {
		t.Fatalf("prepareAttempt error = %v", err)
	}
	if attempt == nil || attempt.channel.ID != 11 {
		t.Fatalf("超深嵌套 group 应被跳过并继续到 channel 11, got %+v", attempt)
	}
	if len(r.iterStack) != 1 {
		t.Fatalf("超深嵌套 group 不应 push 迭代帧, stack len = %d", len(r.iterStack))
	}
	attempts := r.attempts()
	if len(attempts) != 1 || attempts[0].Status != dbmodel.AttemptSkipped {
		t.Fatalf("深度超限的 skip 未正确记录: %+v", attempts)
	}
}

func TestPrepareAttemptStopsWhenRequestContextCanceled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.Request = ctx.Request.WithContext(canceled)

	group := dbmodel.Group{
		Mode:  dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1}},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
	}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		t.Fatal("context 已取消后不应再解析候选")
		return nil, nil
	}

	attempt, err := r.prepareAttempt()
	if attempt != nil {
		t.Fatalf("attempt = %+v, want nil", attempt)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRelayRunStopsOnTerminalClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
		},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{StartTime: time.Now(), RequestModel: "m", ActualModel: "m"},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
		group:           group,
	}
	resolveCount := 0
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		resolveCount++
		return nil, newTerminalRelayError(http.StatusBadRequest, errors.New("bad request"))
	}

	r.run()
	if resolveCount != 1 {
		t.Fatalf("terminal client error should stop failover after one candidate, got %d resolves", resolveCount)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "bad request") {
		t.Fatalf("client-correctable error detail should be preserved: %s", recorder.Body.String())
	}
}

func TestRespondRelayErrorHidesServerDetailsAndLogsFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, observed := observer.New(zap.ErrorLevel)
	previousLogger := projectlog.Logger
	projectlog.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { projectlog.Logger = previousLogger })

	const sentinel = "dial upstream with credential=must-not-reach-client"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	respondRelayError(ctx, http.StatusBadGateway, errors.New(sentinel))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want 502", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), sentinel) {
		t.Fatalf("relay failure leaked internal detail: %s", recorder.Body.String())
	}
	var body serverresp.ResponseStruct
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode relay error response: %v", err)
	}
	if body.Message != relayUpstreamErrorMessage || body.Error == nil || body.Error.Code != relayUpstreamErrorCode {
		t.Fatalf("relay error response = %#v", body)
	}
	if observed.Len() != 1 {
		t.Fatalf("relay error log entries = %d, want 1", observed.Len())
	}
	entry := observed.All()[0]
	errorField, ok := entry.ContextMap()["error"].(string)
	if entry.Message != "relay request failed" || !ok || !strings.Contains(errorField, sentinel) {
		t.Fatalf("structured relay error log = %#v", entry)
	}
}

func TestRelayRunHidesTerminalServerError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	group := dbmodel.Group{
		Mode:  dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1}},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{StartTime: time.Now(), RequestModel: "m", ActualModel: "m"},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
		group:           group,
	}
	const sentinel = "upstream TLS private detail"
	resolveCount := 0
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		resolveCount++
		return nil, newTerminalRelayError(http.StatusServiceUnavailable, errors.New(sentinel))
	}

	r.run()
	if resolveCount != 1 {
		t.Fatalf("terminal server error should stop failover, got %d resolves", resolveCount)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want 503", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), sentinel) || !strings.Contains(recorder.Body.String(), relayUpstreamErrorMessage) {
		t.Fatalf("terminal server error response = %s", recorder.Body.String())
	}
}

func TestRelayRunHidesFinalFailureAndPreservesAttemptMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "m", Priority: 2},
		},
	}
	iter := balancer.NewIterator(group, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{StartTime: time.Now(), RequestModel: "m", ActualModel: "m"},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
		group:           group,
	}
	const sentinel = "channel discovery DSN=must-not-reach-client"
	resolveCount := 0
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		resolveCount++
		r.iter.Skip(item.ChannelID, 0, "unavailable", "candidate resolution failed")
		return nil, errors.New(sentinel)
	}
	failedBefore := op.StatsTotalGet().RequestFailed

	r.run()

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want 502", recorder.Code)
	}
	if resolveCount != 2 {
		t.Fatalf("generic response must not change failover attempts: got %d, want 2", resolveCount)
	}
	if strings.Contains(recorder.Body.String(), sentinel) || !strings.Contains(recorder.Body.String(), relayUpstreamErrorMessage) {
		t.Fatalf("final relay error response = %s", recorder.Body.String())
	}
	attempts := r.attempts()
	if len(attempts) != 2 || attempts[0].AttemptNum != 1 || attempts[1].AttemptNum != 2 {
		t.Fatalf("attempt metrics changed while sanitizing response: %+v", attempts)
	}
	if got := op.StatsTotalGet().RequestFailed; got != failedBefore+1 {
		t.Fatalf("failed request metric delta = %d, want 1", got-failedBefore)
	}
}

func TestRelayRunReturnsGatewayTimeoutForServerDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	requestCtx, cancel := context.WithCancelCause(request.Context())
	cancel(errNonStreamRequestTimeout)
	ctx.Request = request.WithContext(requestCtx)

	r := &relayRun{
		c:       ctx,
		metrics: &RelayMetrics{StartTime: time.Now(), RequestModel: "m", ActualModel: "m"},
	}
	r.run()

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status code = %d, want 504; body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), errNonStreamRequestTimeout.Error()) || !strings.Contains(recorder.Body.String(), relayUpstreamErrorMessage) {
		t.Fatalf("body = %q, want sanitized timeout message", recorder.Body.String())
	}
}

func TestServerDeadlineIsNotClassifiedAsClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errNonStreamRequestTimeout)
	if isRequestContextCanceled(ctx, context.DeadlineExceeded) {
		t.Fatal("server-enforced timeout must not be classified as client cancellation")
	}
}

func TestBuildRealAttemptSkipsCircuitBrokenKeyWithinChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	modelName := "test-key-circuit-build"
	channelID := 91001
	firstKeyID := 92001
	secondKeyID := 92002
	tripCircuitForTest(t, channelID, firstKeyID, modelName)

	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: channelID, ModelName: modelName, Priority: 1},
		},
	}
	iter := balancer.NewIterator(group, 1, modelName)
	if !iter.Next() {
		t.Fatal("test iterator should have one candidate")
	}
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: modelName, RequestType: llm.RequestTypeChat},
		metrics:         &RelayMetrics{RequestModel: modelName, ActualModel: modelName},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
		group:           group,
	}
	channel := &dbmodel.Channel{
		ID:       channelID,
		Name:     "multi-key",
		Type:     llm.APIFormatOpenAIChatCompletion,
		Enabled:  true,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://example.com/v1"}},
		Keys: []dbmodel.ChannelKey{
			{ID: firstKeyID, ChannelID: channelID, Enabled: true, ChannelKey: "sk-first", Remark: "first"},
			{ID: secondKeyID, ChannelID: channelID, Enabled: true, ChannelKey: "sk-second", Remark: "second"},
		},
	}

	attempt, err := r.buildRealAttempt(channel, group.Items[0], false, 0)
	if err != nil {
		t.Fatalf("buildRealAttempt error = %v", err)
	}
	if attempt == nil {
		t.Fatal("buildRealAttempt returned nil, want attempt with second key")
	}
	if attempt.usedKey.ID != secondKeyID || attempt.keyIndex != 1 {
		t.Fatalf("selected key = %d at index %d, want key %d at index 1", attempt.usedKey.ID, attempt.keyIndex, secondKeyID)
	}
	attempts := r.attempts()
	if len(attempts) != 1 || attempts[0].Status != dbmodel.AttemptCircuitBreak || attempts[0].ChannelKeyID != firstKeyID {
		t.Fatalf("first key circuit break not recorded correctly: %+v", attempts)
	}
}

func TestSwitchToNextKeySkipsCircuitBrokenKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	modelName := "test-key-circuit-switch"
	channelID := 91011
	firstKeyID := 92011
	secondKeyID := 92012
	thirdKeyID := 92013
	tripCircuitForTest(t, channelID, secondKeyID, modelName)

	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: channelID, ModelName: modelName, Priority: 1},
		},
	}
	iter := balancer.NewIterator(group, 1, modelName)
	if !iter.Next() {
		t.Fatal("test iterator should have one candidate")
	}
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: modelName, RequestType: llm.RequestTypeChat},
		metrics:         &RelayMetrics{RequestModel: modelName, ActualModel: modelName},
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
		group:           group,
	}
	keyOptions := []dbmodel.ChannelKey{
		{ID: firstKeyID, ChannelID: channelID, Enabled: true, ChannelKey: "sk-first"},
		{ID: secondKeyID, ChannelID: channelID, Enabled: true, ChannelKey: "sk-second", Remark: "blocked"},
		{ID: thirdKeyID, ChannelID: channelID, Enabled: true, ChannelKey: "sk-third", Remark: "third"},
	}
	ra := &relayAttempt{
		relayRun:   r,
		channel:    &dbmodel.Channel{ID: channelID, Name: "multi-key", Type: llm.APIFormatOpenAIChatCompletion},
		usedKey:    keyOptions[0],
		keyOptions: keyOptions,
		keyIndex:   0,
		baseURL:    "https://example.com/v1",
	}

	if !ra.switchToNextKey() {
		t.Fatal("switchToNextKey() = false, want true")
	}
	if ra.usedKey.ID != thirdKeyID || ra.keyIndex != 2 {
		t.Fatalf("selected key = %d at index %d, want key %d at index 2", ra.usedKey.ID, ra.keyIndex, thirdKeyID)
	}
	attempts := r.attempts()
	if len(attempts) != 1 || attempts[0].Status != dbmodel.AttemptCircuitBreak || attempts[0].ChannelKeyID != secondKeyID {
		t.Fatalf("second key circuit break not recorded correctly: %+v", attempts)
	}
}

func tripCircuitForTest(t *testing.T, channelID, keyID int, modelName string) {
	t.Helper()
	balancer.RecordSuccess(channelID, keyID, modelName)
	t.Cleanup(func() {
		balancer.RecordSuccess(channelID, keyID, modelName)
	})
	balancer.RecordFailure(channelID, keyID, modelName)
	balancer.RecordFailure(channelID, keyID, modelName)
}
