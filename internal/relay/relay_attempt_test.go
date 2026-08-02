package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
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
	if m.Stats.InputToken != 0 || m.Stats.OutputToken != 0 || m.Stats.ReasoningToken != 0 {
		t.Fatal("nil usage 必须是空操作")
	}
}

func TestRelaySessionIDUsesStableDigestWithoutPersistingRawValue(t *testing.T) {
	ctx := testGinContext()
	request := &llm.Request{Metadata: map[string]string{"conversation_id": "conversation-42"}}
	first := relaySessionID(ctx, request)
	second := relaySessionID(ctx, request)
	if first == "" || first != second || len(first) != 64 {
		t.Fatalf("session digest = %q/%q, want stable SHA-256 hex", first, second)
	}
	if strings.Contains(first, "conversation-42") {
		t.Fatal("session digest leaked raw conversation ID")
	}
	request.Metadata = map[string]string{"session_id": "\ninvalid"}
	if got := relaySessionID(ctx, request); got != "" {
		t.Fatalf("invalid session ID digest = %q, want empty", got)
	}
}

func TestRelaySessionIDFallsBackToControlledHeader(t *testing.T) {
	ctx := testGinContext()
	ctx.Request.Header.Set("X-Octopus-Session-ID", "header-session")
	got := relaySessionID(ctx, &llm.Request{})
	if got == "" || len(got) != 64 {
		t.Fatalf("header session digest = %q, want SHA-256 hex", got)
	}
}

func TestRecordUsageRecordsTokensWithoutPrice(t *testing.T) {
	// 价格缓存未命中（无 DB 环境）时，仍应记录 token 用量，成本保持 0。
	m := &RelayMetrics{ActualModel: "model-without-price"}
	m.RecordUsage(&llm.Usage{
		PromptTokens:            100,
		CompletionTokens:        40,
		CompletionTokensDetails: &llm.CompletionTokensDetails{ReasoningTokens: 30},
	})
	if m.Stats.InputToken != 100 || m.Stats.OutputToken != 40 || m.Stats.ReasoningToken != 30 {
		t.Fatalf("token 未记录: input=%d output=%d reasoning=%d", m.Stats.InputToken, m.Stats.OutputToken, m.Stats.ReasoningToken)
	}
	if m.Stats.InputCost != 0 || m.Stats.OutputCost != 0 {
		t.Fatalf("无价格时成本应为 0: input=%f output=%f", m.Stats.InputCost, m.Stats.OutputCost)
	}
}

func TestNormalizedReasoningTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage *llm.Usage
		want  int64
	}{
		{name: "nil usage", usage: nil, want: 0},
		{name: "nil details", usage: &llm.Usage{CompletionTokens: 20}, want: 0},
		{name: "negative reasoning", usage: &llm.Usage{CompletionTokens: 20, CompletionTokensDetails: &llm.CompletionTokensDetails{ReasoningTokens: -1}}, want: 0},
		{name: "normal reasoning", usage: &llm.Usage{CompletionTokens: 20, CompletionTokensDetails: &llm.CompletionTokensDetails{ReasoningTokens: 12}}, want: 12},
		{name: "clamped to output", usage: &llm.Usage{CompletionTokens: 20, CompletionTokensDetails: &llm.CompletionTokensDetails{ReasoningTokens: 25}}, want: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedReasoningTokens(tt.usage); got != tt.want {
				t.Fatalf("normalizedReasoningTokens() = %d, want %d", got, tt.want)
			}
		})
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

func TestFinalAttemptKeepsFailedChannelWhenLaterCandidatesAreSkipped(t *testing.T) {
	attempts := []dbmodel.ChannelAttempt{
		{ChannelID: 41, ChannelName: "Linuxdo_Andrew", ChannelKeyID: 9, Status: dbmodel.AttemptFailed},
		{ChannelID: 42, ChannelName: "Linuxdo_无名", Status: dbmodel.AttemptSkipped, Msg: "no available key"},
	}
	id, name, keyID, status := finalAttempt(attempts)
	if id != 41 || name != "Linuxdo_Andrew" || keyID != 9 || status != dbmodel.AttemptFailed {
		t.Fatalf("got id=%d name=%s key=%d status=%s, want the actual failed channel", id, name, keyID, status)
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

func TestRelayRunTimelinePreservesNestedEventOrder(t *testing.T) {
	parentGroup := dbmodel.Group{Items: []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 10},
		{ID: 2, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 20},
	}}
	childGroup := dbmodel.Group{Items: []dbmodel.GroupItem{{ID: 3, ChannelID: 30, ModelName: "m"}}}
	parentIter := balancer.NewIterator(parentGroup, 1, "m")
	childIter := balancer.NewIterator(childGroup, 1, "m")
	r := &relayRun{}
	r.attachIteratorTimeline(parentIter)
	r.attachIteratorTimeline(childIter)

	if !parentIter.Next() {
		t.Fatal("parent iterator missing first nested group")
	}
	parentIter.RedirectFor(parentIter.Item(), 0, "root", 10, "child-a", 1, "enter")
	if !childIter.Next() {
		t.Fatal("child iterator missing channel")
	}
	childIter.Skip(30, 0, "child-channel", "failed child candidate")
	if !parentIter.Next() {
		t.Fatal("parent iterator missing second nested group")
	}
	parentIter.RedirectFor(parentIter.Item(), 0, "root", 20, "child-b", 1, "enter")

	attempts := r.attempts()
	if len(attempts) != 3 {
		t.Fatalf("timeline length = %d, want 3", len(attempts))
	}
	if attempts[0].Status != dbmodel.AttemptRedirect || attempts[1].ChannelID != 30 || attempts[2].Status != dbmodel.AttemptRedirect {
		t.Fatalf("timeline order = %+v, want redirect -> child event -> redirect", attempts)
	}
	for i, attempt := range attempts {
		if attempt.AttemptNum != i+1 {
			t.Fatalf("attempt %d number = %d, want %d", i, attempt.AttemptNum, i+1)
		}
	}
}

func TestPrepareAttemptUsesNestedGroupFirstTokenPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := testGinContext()
	root := dbmodel.Group{
		ID:                1,
		Name:              "root",
		FirstTokenTimeOut: 60,
	}
	child := dbmodel.Group{
		ID:                2,
		Name:              "child",
		FirstTokenTimeOut: 2,
		Items: []dbmodel.GroupItem{{
			ID:        1,
			Type:      dbmodel.GroupItemTypeChannel,
			ChannelID: 10,
			ModelName: "m",
		}},
	}
	childIter := balancer.NewIterator(child, 1, "m")
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{},
		group:           root,
		iter:            childIter,
		iterStack:       []*relayIteratorFrame{{group: child, iter: childIter, depth: 1}},
		iterHistory:     []*balancer.Iterator{childIter},
	}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID}}, nil
	}

	attempt, err := r.prepareAttempt()
	if err != nil {
		t.Fatalf("prepareAttempt error = %v", err)
	}
	if attempt == nil {
		t.Fatal("prepareAttempt returned nil attempt")
	}
	timeout := attempt.firstTokenTimeout()
	if timeout.Source != firstTokenTimeoutManual || timeout.Duration != 2*time.Second {
		t.Fatalf("child timeout = %+v, want child manual timeout of 2s", timeout)
	}
}

func TestRelayRunContinuesFromParentClientStyleErrorIntoNestedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "nested-runner.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	parent := dbmodel.Group{
		ID:   1,
		Name: "parent",
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "m", Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 2, Priority: 2},
		},
	}
	childChannel := dbmodel.Channel{
		Name:     "child-channel",
		Type:     llm.APIFormatOpenAIChatCompletion,
		Enabled:  true,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://child.example"}},
		Keys:     []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "child-key"}},
	}
	if err := op.ChannelCreate(&childChannel, context.Background()); err != nil {
		t.Fatalf("create child channel: %v", err)
	}
	child := dbmodel.Group{
		ID:      2,
		Name:    "child",
		Enabled: true,
		Mode:    dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{
			Type:      dbmodel.GroupItemTypeChannel,
			ChannelID: childChannel.ID,
			ModelName: "m",
			Priority:  1,
		}},
	}
	if err := op.GroupCreate(&child, context.Background()); err != nil {
		t.Fatalf("create child group: %v", err)
	}

	iter := newRelayIterator(parent, 1, &llm.Request{Model: "m"}, context.Background())
	r := &relayRun{
		c:               ctx,
		internalRequest: &llm.Request{Model: "m"},
		metrics:         &RelayMetrics{},
		group:           parent,
		iter:            iter,
		iterStack:       []*relayIteratorFrame{{group: parent, iter: iter, depth: 0}},
		iterHistory:     []*balancer.Iterator{iter},
	}

	visited := []int{}
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		visited = append(visited, item.ChannelID)
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID, Name: "channel"}}, nil
	}
	r.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		if attempt.channel.ID == 10 {
			return false, errors.New("channel parent failed: upstream invalid_request_error")
		}
		return false, nil
	}

	r.run()
	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want success after nested fallback", recorder.Code)
	}
	if len(visited) != 2 || visited[0] != 10 || visited[1] != childChannel.ID {
		t.Fatalf("visited channels = %+v, want parent channel then nested child channel %d", visited, childChannel.ID)
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

func TestRelayRunReturnsRetryableClientErrorAfterCandidatesExhausted(t *testing.T) {
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
	r.resolveGroupItemFunc = func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error) {
		return &relayAttempt{relayRun: r, channel: &dbmodel.Channel{ID: item.ChannelID}}, nil
	}
	r.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
		return false, &classifiedClientRelayError{
			cause: errors.New("provider detail"), reason: "upstream tool call state mismatch", statusCode: http.StatusBadRequest,
		}
	}

	r.run()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want preserved 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "tool call state mismatch") || strings.Contains(recorder.Body.String(), "provider detail") {
		t.Fatalf("client response did not preserve safe classified reason: %s", recorder.Body.String())
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
