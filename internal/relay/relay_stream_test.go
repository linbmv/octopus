package relay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/routingstate"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestNestedGroupPriorityParticipatesWithParentCandidates(t *testing.T) {
	parent := dbmodel.Group{
		ID:   1,
		Name: "opus",
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 2, Priority: 1},
			{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, ModelName: "opus-a", Priority: 2},
			{ID: 3, Type: dbmodel.GroupItemTypeChannel, ChannelID: 11, ModelName: "opus-b", Priority: 3},
		},
	}
	child := dbmodel.Group{
		ID:   2,
		Name: "gpt",
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 4, Type: dbmodel.GroupItemTypeChannel, ChannelID: 20, ModelName: "gpt-slow", Priority: 20},
			{ID: 5, Type: dbmodel.GroupItemTypeChannel, ChannelID: 21, ModelName: "gpt-fast", Priority: 10},
		},
	}

	orderedParent := nestedFallbackCandidates(parent)
	if orderedParent[0].Type != dbmodel.GroupItemTypeGroup || orderedParent[0].TargetGroupID != 2 {
		t.Fatalf("嵌套分组应按父级 priority 参与排序并成为第一个候选, got %+v", orderedParent)
	}
	if orderedParent[1].ChannelID != 10 || orderedParent[2].ChannelID != 11 {
		t.Fatalf("父级直连渠道应按自己的 priority 排在嵌套分组之后, got %+v", orderedParent)
	}

	parentIter := newRelayIterator(parent, 1, &llm.Request{Model: "opus"}, context.Background())
	if !parentIter.Next() || parentIter.Item().Type != dbmodel.GroupItemTypeGroup {
		t.Fatalf("父分组第一个候选 = %+v, 期望 priority 更高的嵌套分组", parentIter.Item())
	}
	if !parentIter.Next() || parentIter.Item().ChannelID != 10 {
		t.Fatalf("父分组第二个候选 = %+v, 期望直连 opus-a 候选", parentIter.Item())
	}
	if !parentIter.Next() || parentIter.Item().ChannelID != 11 {
		t.Fatalf("父分组第三个候选 = %+v, 期望直连 opus-b 候选", parentIter.Item())
	}

	childIter := newRelayIterator(child, 1, &llm.Request{Model: "opus"}, context.Background())
	if !childIter.Next() || childIter.Item().ChannelID != 21 {
		t.Fatalf("子分组应保留自己的 failover priority, 第一个候选 = %+v", childIter.Item())
	}
	if !childIter.Next() || childIter.Item().ChannelID != 20 {
		t.Fatalf("子分组第二个候选 = %+v, 期望 gpt-slow", childIter.Item())
	}
}

func TestSafeKeyRemark(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"空字符串", "", "-"},
		{"仅空白", "   ", "-"},
		{"控制字符被剥离", "ab\x00\x07cd", "abcd"},
		{"正常备注", "prod-key", "prod-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeKeyRemark(tt.in); got != tt.want {
				t.Fatalf("safeKeyRemark(%q) = %q, 期望 %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSafeKeyRemarkTruncatesLongInput(t *testing.T) {
	got := safeKeyRemark(strings.Repeat("x", 100))
	if len([]rune(got)) != 64+3 {
		t.Fatalf("长度 = %d, 期望 67 (64 字符 + 省略号)", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("期望截断省略号, got %q", got)
	}
}

func TestCleanKeyRemark(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// 与 safeKeyRemark 的差异：空备注返回空字符串，便于持久化层用 omitempty 省略。
		{"空字符串", "", ""},
		{"仅空白", "   ", ""},
		{"控制字符被剥离", "ab\x00\x07cd", "abcd"},
		{"正常备注", "linwolfer", "linwolfer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanKeyRemark(tt.in); got != tt.want {
				t.Fatalf("cleanKeyRemark(%q) = %q, 期望 %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanKeyRemarkTruncatesLongInput(t *testing.T) {
	got := cleanKeyRemark(strings.Repeat("x", 100))
	if len([]rune(got)) != 64+3 {
		t.Fatalf("长度 = %d, 期望 67 (64 字符 + 省略号)", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("期望截断省略号, got %q", got)
	}
}

// TestStreamAggregationProducesBodyAndUsage 锁定 writeStream 依赖的核心契约：
// 入站聚合器能把客户端格式的流式分片合成完整响应体并提取最终 usage。
func TestStreamAggregationProducesBodyAndUsage(t *testing.T) {
	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	if inbound == nil {
		t.Fatal("newInbound 返回 nil")
	}
	events := []*httpclient.StreamEvent{
		{Data: []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`)},
		{Data: []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)},
		{Data: []byte("[DONE]")},
	}
	body, meta, err := inbound.AggregateStreamChunks(context.Background(), events)
	if err != nil {
		t.Fatalf("聚合错误: %v", err)
	}
	if !strings.Contains(string(body), "Hello world") {
		t.Fatalf("聚合响应体缺少内容: %s", body)
	}
	if meta.Usage == nil || meta.Usage.PromptTokens != 5 || meta.Usage.CompletionTokens != 2 {
		t.Fatalf("usage 未聚合: %+v", meta.Usage)
	}
}

// TestStreamAggregationFeedsMetricsUsage 模拟 writeStream 结束分支：
// 聚合得到的 body 与 usage 应能正确落到 RelayMetrics。
func TestStreamAggregationFeedsMetricsUsage(t *testing.T) {
	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	events := []*httpclient.StreamEvent{
		{Data: []byte(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12,"completion_tokens_details":{"reasoning_tokens":2}}}`)},
	}
	body, meta, err := inbound.AggregateStreamChunks(context.Background(), events)
	if err != nil {
		t.Fatalf("聚合错误: %v", err)
	}

	m := &RelayMetrics{ActualModel: "no-price-model"}
	m.InternalResponse = body
	m.RecordUsage(meta.Usage)

	if m.Stats.InputToken != 9 || m.Stats.OutputToken != 3 || m.Stats.ReasoningToken != 2 {
		t.Fatalf("metrics usage = in:%d out:%d reasoning:%d, 期望 9/3/2", m.Stats.InputToken, m.Stats.OutputToken, m.Stats.ReasoningToken)
	}
	if len(m.InternalResponse) == 0 {
		t.Fatal("InternalResponse 应保存聚合后的响应体")
	}
}

func TestStreamLogCollectorKeepsShortStreamAggregatable(t *testing.T) {
	collector := newStreamLogCollector()
	collector.Add(&httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)})

	if collector.Truncated() {
		t.Fatal("short stream should not be truncated")
	}
	if len(collector.Events()) != 1 {
		t.Fatalf("events length = %d, want 1", len(collector.Events()))
	}
	if collector.Usage() == nil || collector.Usage().PromptTokens != 4 || collector.Usage().CompletionTokens != 2 {
		t.Fatalf("usage not tracked: %+v", collector.Usage())
	}
}

func TestStreamLogCollectorBoundsLongStreamAndKeepsUsage(t *testing.T) {
	collector := newStreamLogCollector()
	collector.Add(&httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":"` + strings.Repeat("x", conf.MaxRelayLogContentBytes) + `"}}]}`)})
	collector.Add(&httpclient.StreamEvent{Data: []byte(`{"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)})

	if !collector.Truncated() {
		t.Fatal("long stream should be truncated")
	}
	if len(collector.Events()) != 0 {
		t.Fatalf("truncated collector should release retained events, got %d", len(collector.Events()))
	}
	if len(collector.TruncatedBody()) > conf.MaxRelayLogContentBytes {
		t.Fatalf("truncated body length = %d, want <= %d", len(collector.TruncatedBody()), conf.MaxRelayLogContentBytes)
	}
	if !strings.Contains(string(collector.TruncatedBody()), streamLogTruncatedMarker) {
		t.Fatalf("truncated body missing marker")
	}
	if collector.Usage() == nil || collector.Usage().PromptTokens != 11 || collector.Usage().CompletionTokens != 7 {
		t.Fatalf("usage from post-truncation event not tracked: %+v", collector.Usage())
	}
}

func TestStreamLogCollectorRecognizesOnlySuccessfulTerminalEvents(t *testing.T) {
	tests := []struct {
		name  string
		event *httpclient.StreamEvent
		want  bool
	}{
		{name: "chat done", event: &httpclient.StreamEvent{Data: llm.DoneStreamEvent.Data}, want: true},
		{name: "responses completed", event: &httpclient.StreamEvent{Type: "response.completed"}, want: true},
		{name: "anthropic message stop", event: &httpclient.StreamEvent{Type: "message_stop"}, want: true},
		{name: "speech done", event: &httpclient.StreamEvent{Type: "speech.audio.done"}, want: true},
		{name: "transcript done", event: &httpclient.StreamEvent{Type: "transcript.text.done"}, want: true},
		{name: "binary done", event: &httpclient.StreamEvent{Type: httpclient.BinaryStreamDoneEventType}, want: true},
		{name: "responses failed", event: &httpclient.StreamEvent{Type: "response.failed"}, want: false},
		{name: "responses incomplete", event: &httpclient.StreamEvent{Type: "response.incomplete"}, want: false},
		{name: "ordinary delta", event: &httpclient.StreamEvent{Type: "response.output_text.delta", Data: []byte(`{"delta":"hi"}`)}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newStreamLogCollector()
			collector.Add(tt.event)
			if got := collector.Completed(); got != tt.want {
				t.Fatalf("Completed() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestStreamEventSemanticOutputBoundary(t *testing.T) {
	tests := []struct {
		name  string
		event *httpclient.StreamEvent
		want  bool
	}{
		{name: "responses created control", event: &httpclient.StreamEvent{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"status":"in_progress"}}`)}, want: false},
		{name: "responses in progress control", event: &httpclient.StreamEvent{Type: "response.in_progress", Data: []byte(`{"type":"response.in_progress"}`)}, want: false},
		{name: "chat role control", event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"role":"assistant"}}]}`)}, want: false},
		{name: "chat whitespace content", event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":" "}}]}`)}, want: true},
		{name: "chat text", event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)}, want: true},
		{name: "chat tool call", event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"name":"lookup"}}]}}]}`)}, want: true},
		{name: "responses text delta", event: &httpclient.StreamEvent{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"hello"}`)}, want: true},
		{name: "responses function item", event: &httpclient.StreamEvent{Type: "response.output_item.added", Data: []byte(`{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","call_id":"call_1"}}`)}, want: true},
		{name: "responses citation annotation", event: &httpclient.StreamEvent{Type: "response.output_text.annotation.added", Data: []byte(`{"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com"}}`)}, want: true},
		{name: "anthropic message start", event: &httpclient.StreamEvent{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"content":[]}}`)}, want: false},
		{name: "anthropic empty text block", event: &httpclient.StreamEvent{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","content_block":{"type":"text","text":""}}`)}, want: false},
		{name: "anthropic tool block", event: &httpclient.StreamEvent{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_1","name":"lookup"}}`)}, want: true},
		{name: "anthropic text delta", event: &httpclient.StreamEvent{Type: "content_block_delta", Data: []byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`)}, want: true},
		{name: "gemini text", event: &httpclient.StreamEvent{Data: []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)}, want: true},
		{name: "gemini function call", event: &httpclient.StreamEvent{Data: []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"city":"Paris"}}}]}}]}`)}, want: true},
		{name: "usage only", event: &httpclient.StreamEvent{Data: []byte(`{"usage":{"input_tokens":1,"output_tokens":0}}`)}, want: false},
		{name: "custom json payload", event: &httpclient.StreamEvent{Type: "chunk", Data: []byte(`{"chunk":"hello"}`)}, want: true},
		{name: "done marker", event: &httpclient.StreamEvent{Data: llm.DoneStreamEvent.Data}, want: false},
		{name: "binary audio", event: &httpclient.StreamEvent{Type: "audio/mpeg", Data: []byte{1, 2, 3}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := streamEventHasSemanticOutput(tt.event); got != tt.want {
				t.Fatalf("streamEventHasSemanticOutput() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestReadStreamStopsGuardBeforeEnqueueingSemanticOutput(t *testing.T) {
	stream := &fakeStream{events: []*httpclient.StreamEvent{{
		Data: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`),
	}}}
	results := make(chan sseReadResult)
	stopped := make(chan struct{})
	var stopOnce sync.Once
	go readStream(
		context.Background(), stream, results,
		func() { stopOnce.Do(func() { close(stopped) }) }, func() {},
	)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("semantic event did not stop the guard before enqueue")
	}
	result, ok := <-results
	if !ok || result.event == nil || !result.semanticChecked || !result.semanticOutput {
		t.Fatalf("semantic read result = %#v, open=%t", result, ok)
	}
	for range results {
	}
}

func TestProcessStreamEventsBuffersControlsUntilSemanticOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ra := &relayAttempt{relayRun: &relayRun{
		c: ginCtx, metrics: &RelayMetrics{ActualModel: "semantic-test"},
		inAdapter: newInbound(llm.APIFormatOpenAIResponse),
	}}
	results := make(chan sseReadResult, 3)
	results <- sseReadResult{event: &httpclient.StreamEvent{
		Type: "response.created", Data: []byte(`{"type":"response.created","response":{"status":"in_progress"}}`),
	}}
	results <- sseReadResult{event: &httpclient.StreamEvent{
		Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"hello"}`),
	}}
	close(results)
	firstToken := true
	guardStops := 0
	err := ra.processStreamEvents(
		ra.c.Request.Context(), results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() { guardStops++ }, func() {}, 0, nil,
	)
	if err != nil {
		t.Fatalf("processStreamEvents() error = %v", err)
	}
	if firstToken || guardStops != 1 {
		t.Fatalf("semantic boundary state: first=%t guard_stops=%d", firstToken, guardStops)
	}
	if !ra.c.Writer.Written() {
		t.Fatal("semantic output did not flush buffered events")
	}
	body := recorder.Body.String()
	controlIndex := strings.Index(body, "response.created")
	semanticIndex := strings.Index(body, "response.output_text.delta")
	if controlIndex < 0 || semanticIndex < 0 || controlIndex >= semanticIndex {
		t.Fatalf("buffered event order is incorrect: %q", body)
	}
}

func TestProcessStreamEventsDoesNotCommitSemanticOutputAfterRoutingChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errRoutingConfigChanged)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	ra := &relayAttempt{relayRun: &relayRun{
		c: ginCtx, metrics: &RelayMetrics{ActualModel: "routing-race-test"},
		inAdapter: newInbound(llm.APIFormatOpenAIResponse),
	}}
	results := make(chan sseReadResult, 1)
	results <- sseReadResult{
		event: &httpclient.StreamEvent{
			Type: "response.output_text.delta",
			Data: []byte(`{"type":"response.output_text.delta","delta":"hello"}`),
		},
		semanticOutput: true, semanticChecked: true,
	}
	close(results)
	firstToken := true
	err := ra.processStreamEvents(
		ctx, results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() {}, func() {}, 0, nil,
	)
	if !errors.Is(err, errRoutingConfigChanged) {
		t.Fatalf("processStreamEvents() error = %v, want routing change", err)
	}
	if !firstToken || recorder.Body.Len() != 0 || ginCtx.Writer.Written() {
		t.Fatalf("routing change lost semantic boundary race: first=%t body=%q", firstToken, recorder.Body.String())
	}
}

func TestProcessStreamEventsFlushesControlOnlyStreamOnCleanEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	metrics := &RelayMetrics{ActualModel: "control-only-test"}
	ra := &relayAttempt{relayRun: &relayRun{
		c: ginCtx, metrics: metrics,
		inAdapter: newInbound(llm.APIFormatOpenAIResponse),
	}}
	results := make(chan sseReadResult, 1)
	results <- sseReadResult{event: &httpclient.StreamEvent{
		Type: "response.incomplete",
		Data: []byte(`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`),
	}}
	close(results)
	firstToken := true
	guardStops := 0
	err := ra.processStreamEvents(
		ra.c.Request.Context(), results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() { guardStops++ }, func() {}, 0, nil,
	)
	if err != nil {
		t.Fatalf("processStreamEvents() error = %v", err)
	}
	if !firstToken || guardStops != 1 || !metrics.FirstTokenTime.IsZero() {
		t.Fatalf("control-only boundary state: first=%t guard_stops=%d first_token=%v",
			firstToken, guardStops, metrics.FirstTokenTime)
	}
	if !strings.Contains(recorder.Body.String(), "response.incomplete") {
		t.Fatalf("clean EOF did not flush control-only response: %q", recorder.Body.String())
	}
}

func TestPreSemanticStreamBufferIsBounded(t *testing.T) {
	buffer := &preSemanticStreamBuffer{}
	err := buffer.add(&httpclient.StreamEvent{Type: "response.created", Data: bytes.Repeat([]byte("x"), conf.MaxRelayLogContentBytes)})
	if !errors.Is(err, errPreSemanticStreamBufferExceeded) {
		t.Fatalf("buffer.add() error = %v, want pre-semantic buffer limit", err)
	}
}

type blockingAfterEventsStream struct {
	events      []*httpclient.StreamEvent
	index       int
	release     chan struct{}
	closeOnce   sync.Once
	delivered   chan struct{}
	deliverOnce sync.Once
	ctx         context.Context
}

func (s *blockingAfterEventsStream) Next() bool {
	if s.index < len(s.events) {
		s.index++
		if s.delivered != nil {
			s.deliverOnce.Do(func() { close(s.delivered) })
		}
		return true
	}
	<-s.release
	return false
}

func TestRoutingChangeAfterControlEventRemainsSafeToFailOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	snapshot := routingstate.Current()
	guarded, stopGuard, releaseGuard := newRoutingChangeGuard(context.Background(), snapshot.Changed)
	defer releaseGuard()

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(guarded)
	delivered := make(chan struct{})
	stream := &blockingAfterEventsStream{
		events: []*httpclient.StreamEvent{{
			Type: "response.created", Data: []byte(`{"type":"response.created","response":{"status":"in_progress"}}`),
		}},
		release: make(chan struct{}), delivered: delivered, ctx: guarded,
	}
	ra := &relayAttempt{relayRun: &relayRun{
		c: ginCtx, metrics: &RelayMetrics{ActualModel: "control-event-test"},
		inAdapter: newInbound(llm.APIFormatOpenAIResponse),
	}}

	done := make(chan error, 1)
	go func() {
		done <- ra.writeStream(guarded, stopGuard, firstTokenTimeoutConfig{}, stream)
	}()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("control event was not read")
	}
	// Let the relay enqueue and buffer the control event. It must still have
	// written no response bytes when the administrator changes routing.
	time.Sleep(10 * time.Millisecond)
	if recorder.Body.Len() != 0 || ginCtx.Writer.Written() {
		t.Fatalf("control event crossed failover boundary: %q", recorder.Body.String())
	}
	routingstate.Notify()

	select {
	case err := <-done:
		if !errors.Is(err, errRoutingConfigChanged) {
			t.Fatalf("writeStream() error = %v, want routing configuration change", err)
		}
	case <-time.After(time.Second):
		t.Fatal("routing change did not interrupt stream after control event")
	}
	if recorder.Body.Len() != 0 || ginCtx.Writer.Written() {
		t.Fatalf("discarded control event was written to the client: %q", recorder.Body.String())
	}
}

func (s *blockingAfterEventsStream) Current() *httpclient.StreamEvent {
	if s.index == 0 || s.index > len(s.events) {
		return nil
	}
	return s.events[s.index-1]
}

func (s *blockingAfterEventsStream) Err() error {
	if s.ctx == nil {
		return nil
	}
	return s.ctx.Err()
}

func (s *blockingAfterEventsStream) Close() error {
	s.closeOnce.Do(func() { close(s.release) })
	return nil
}

type cancelOnTerminalFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelOnTerminalFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), "response.completed") {
		w.once.Do(w.cancel)
	}
}

func TestWriteStreamTreatsDisconnectAfterResponsesCompletedAsSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), time.Second)
	defer deadlineCancel()
	ctx, cancel := context.WithCancel(deadlineCtx)
	recorder := &cancelOnTerminalFlushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		cancel:           cancel,
	}
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

	completed := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_test","object":"response","created_at":1700000000,"model":"gpt-test","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":15}}}`)
	stream := &blockingAfterEventsStream{
		events:  []*httpclient.StreamEvent{{Type: "response.completed", Data: completed}},
		release: make(chan struct{}),
		ctx:     ctx,
	}
	metrics := &RelayMetrics{ActualModel: "no-price-model"}
	ra := &relayAttempt{
		relayRun: &relayRun{
			c:         ginCtx,
			metrics:   metrics,
			inAdapter: newInbound(llm.APIFormatOpenAIResponse),
		},
	}

	if err := ra.writeStream(ctx, func() {}, firstTokenTimeoutConfig{}, stream); err != nil {
		t.Fatalf("writeStream() error = %v, want completed stream success", err)
	}
	if ctx.Err() == nil {
		t.Fatal("test did not cancel the request after writing response.completed")
	}
	if metrics.Stats.InputToken != 12 || metrics.Stats.OutputToken != 3 || metrics.Stats.ReasoningToken != 2 {
		t.Fatalf("usage = %d/%d/%d, want 12/3/2", metrics.Stats.InputToken, metrics.Stats.OutputToken, metrics.Stats.ReasoningToken)
	}
	if len(metrics.InternalResponse) == 0 {
		t.Fatal("completed stream response was not aggregated")
	}
}

func TestWriteStreamReturnsCanceledOnClientDisconnectAfterFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancelCause(context.Background())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	stream := &fakeStream{events: []*httpclient.StreamEvent{
		{Data: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)},
	}}
	ra := &relayAttempt{
		relayRun: &relayRun{
			c:       ginCtx,
			metrics: &RelayMetrics{},
		},
	}

	cancel(context.Canceled)
	err := ra.writeStream(ctx, func() {}, firstTokenTimeoutConfig{}, stream)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeStream error = %v, want context.Canceled", err)
	}
	// 关闭可能由主流程或 reader 协程执行（sync.Once 收敛），轮询等待其收敛，
	// 避免依赖具体哪个协程赢得 Once 而产生 flaky。
	closed := false
	for i := 0; i < 100; i++ {
		if stream.closed.Load() {
			closed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !closed {
		t.Fatal("stream should be closed after client disconnect")
	}
}

func TestStreamIdleTimeoutArmsOnlyAfterFirstNonEmptyEvent(t *testing.T) {
	ra := newIdleTestAttempt(t, llm.APIFormatOpenAIChatCompletion)
	results := make(chan sseReadResult, 1)
	results <- sseReadResult{event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)}}
	firstToken := true
	closed := atomic.Bool{}
	start := time.Now()
	err := ra.processStreamEvents(
		ra.c.Request.Context(), results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() {}, func() { closed.Store(true) }, 80*time.Millisecond, nil,
	)
	if !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("processStreamEvents() error = %v, want stream idle timeout", err)
	}
	if firstToken || !closed.Load() || time.Since(start) < 60*time.Millisecond {
		t.Fatalf("idle guard state: first=%t closed=%t elapsed=%s", firstToken, closed.Load(), time.Since(start))
	}
}

func TestProcessStreamEventsReturnsClassifiableErrorBeforeWritingFirstEvent(t *testing.T) {
	ra := newIdleTestAttempt(t, llm.APIFormatAnthropicMessage)
	results := make(chan sseReadResult, 1)
	results <- sseReadResult{event: &httpclient.StreamEvent{
		Type: "error",
		Data: []byte(`{"type":"error","error":{"type":"1308","message":"usage limit reached"}}`),
	}}
	close(results)
	firstToken := true
	closed := false
	err := ra.processStreamEvents(
		ra.c.Request.Context(),
		results,
		&firstToken,
		newStreamLogCollector(),
		firstTokenTimeoutConfig{},
		func() {},
		func() { closed = true },
		0,
		nil,
	)
	var softErr *streamSoftError
	if !errors.As(err, &softErr) {
		t.Fatalf("processStreamEvents() error = %v, want streamSoftError", err)
	}
	if !closed || ra.c.Writer.Written() {
		t.Fatalf("error event boundary: closed=%t written=%t", closed, ra.c.Writer.Written())
	}
	if !bytes.Contains(softErr.Body(), []byte(`"type":"1308"`)) {
		t.Fatalf("classification body = %s", softErr.Body())
	}
	decision := decideRelayError(http.StatusOK, nil, softErr.Body(), err)
	if decision.Classification.Level != errorclass.ErrorLevelKey || !decision.RetryNextKey {
		t.Fatalf("decision = %#v, want key-level retry", decision)
	}
}

func TestStreamErrorEventDetectionIgnoresSuccessfulPayloads(t *testing.T) {
	for _, event := range []*httpclient.StreamEvent{
		{Type: "message", Data: []byte(`{"content":"hello","error":null}`)},
		{Type: "response.completed", Data: []byte(`{"type":"response.completed","response":{"status":"completed"}}`)},
		{Data: []byte(`{"choices":[{"delta":{"content":"rate limit documentation"}}]}`)},
	} {
		if body, ok := streamErrorEventBody(event); ok {
			t.Fatalf("successful event detected as error: event=%+v body=%s", event, body)
		}
	}
}

func TestStreamIdleTimeoutResetsOnEventAndRawHeartbeatActivity(t *testing.T) {
	ra := newIdleTestAttempt(t, llm.APIFormatOpenAIChatCompletion)
	results := make(chan sseReadResult, 4)
	activity := make(chan struct{}, 4)
	results <- sseReadResult{event: &httpclient.StreamEvent{Data: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)}}
	firstToken := true
	start := time.Now()
	go func() {
		time.Sleep(40 * time.Millisecond)
		results <- sseReadResult{event: &httpclient.StreamEvent{Data: nil}} // decoded heartbeat event
		time.Sleep(40 * time.Millisecond)
		activity <- struct{}{} // raw comment heartbeat consumed by the decoder
		time.Sleep(40 * time.Millisecond)
		activity <- struct{}{}
	}()
	err := ra.processStreamEvents(
		ra.c.Request.Context(), results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() {}, func() {}, 80*time.Millisecond, activity,
	)
	if !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("processStreamEvents() error = %v, want stream idle timeout", err)
	}
	if elapsed := time.Since(start); elapsed < 175*time.Millisecond {
		t.Fatalf("heartbeats did not reset idle timeout; elapsed=%s", elapsed)
	}
}

func TestStreamTerminalEventStillSucceedsWhenIdleGuardFires(t *testing.T) {
	ra := newIdleTestAttempt(t, llm.APIFormatOpenAIResponse)
	completed := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_idle","object":"response","created_at":1700000000,"model":"gpt-test","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`)
	results := make(chan sseReadResult, 1)
	results <- sseReadResult{event: &httpclient.StreamEvent{Type: "response.completed", Data: completed}}
	firstToken := true
	err := ra.processStreamEvents(
		ra.c.Request.Context(), results, &firstToken, newStreamLogCollector(), firstTokenTimeoutConfig{},
		func() {}, func() {}, 50*time.Millisecond, nil,
	)
	if err != nil {
		t.Fatalf("terminal event with idle guard error = %v, want success", err)
	}
	if ra.metrics.Stats.InputToken != 2 || ra.metrics.Stats.OutputToken != 1 {
		t.Fatalf("terminal usage = %d/%d, want 2/1", ra.metrics.Stats.InputToken, ra.metrics.Stats.OutputToken)
	}
}

func newIdleTestAttempt(t *testing.T, format llm.APIFormat) *relayAttempt {
	t.Helper()
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/stream", nil)
	return &relayAttempt{relayRun: &relayRun{
		c:         ctx,
		metrics:   &RelayMetrics{ActualModel: "idle-test"},
		inAdapter: newInbound(format),
	}}
}

// newPassthroughAttempt 构造一个带入站请求与渠道的 relayAttempt，用于验证 raw passthrough 副作用。
func newPassthroughAttempt(channel *dbmodel.Channel, internalRequest *llm.Request) *relayAttempt {
	return &relayAttempt{
		relayRun: &relayRun{
			metrics:         &RelayMetrics{ActualModel: internalRequest.Model},
			internalRequest: internalRequest,
		},
		channel: channel,
	}
}

func boolPtr(b bool) *bool { return &b }

// jsonRequest 构造一个 JSON 出站请求，body 默认是 outbound transformer 重序列化后的形态（字段顺序被打乱）。
func jsonRequest(body string) *httpclient.Request {
	return &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(body),
	}
}
