package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// newTestAttempt 构造一个仅持有 metrics 与 channel 的 relayAttempt，
// 用于在不触发负载均衡循环和真实 HTTP 转发的前提下验证通道级副作用。
func newTestAttempt(channel *dbmodel.Channel) *relayAttempt {
	return &relayAttempt{
		relayRun: &relayRun{
			metrics: &RelayMetrics{ActualModel: "test-model"},
		},
		channel: channel,
	}
}

func TestApplyChannelRequestOptionsOverridesJSONBody(t *testing.T) {
	override := `{"temperature":0.5,"top_p":0.9}`
	ra := newTestAttempt(&dbmodel.Channel{ParamOverride: &override})

	req := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"model":"m","temperature":1}`),
	}
	ra.applyChannelRequestOptions(req)

	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("body 不是合法 JSON: %v", err)
	}
	if got["temperature"] != 0.5 {
		t.Fatalf("temperature = %v, 期望被覆盖为 0.5", got["temperature"])
	}
	if got["top_p"] != 0.9 {
		t.Fatalf("top_p = %v, 期望被追加为 0.9", got["top_p"])
	}
	if got["model"] != "m" {
		t.Fatalf("model = %v, 期望保留原值 m", got["model"])
	}
	if ra.metrics.ParamOverride != override {
		t.Fatalf("metrics.ParamOverride = %q, 期望 %q", ra.metrics.ParamOverride, override)
	}
}

func TestApplyChannelRequestOptionsSkipsNonJSONBody(t *testing.T) {
	override := `{"temperature":0.5}`
	ra := newTestAttempt(&dbmodel.Channel{ParamOverride: &override})

	original := []byte("binary-multipart-data")
	req := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"multipart/form-data; boundary=xyz"}},
		ContentType: "multipart/form-data; boundary=xyz",
		Body:        append([]byte(nil), original...),
	}
	ra.applyChannelRequestOptions(req)

	// 图片编辑等 multipart 请求不能按 JSON map 合并，body 必须保持原样。
	if !bytes.Equal(req.Body, original) {
		t.Fatalf("非 JSON 请求体被修改: %q", req.Body)
	}
	if ra.metrics.ParamOverride != "" {
		t.Fatalf("metrics.ParamOverride = %q, 非 JSON 请求期望为空", ra.metrics.ParamOverride)
	}
}

func TestApplyChannelRequestOptionsKeepsExistingSensitiveHeader(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{
		CustomHeader: []dbmodel.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: "Bearer custom-should-not-win"},
			{HeaderKey: "X-Custom-Tag", HeaderValue: "octopus"},
		},
	})

	req := &httpclient.Request{
		Headers: http.Header{"Authorization": {"Bearer original-auth"}},
	}
	ra.applyChannelRequestOptions(req)

	// pipeline 已先写入认证头；同名敏感头必须保持认证配置优先。
	if got := req.Headers.Get("Authorization"); got != "Bearer original-auth" {
		t.Fatalf("Authorization = %q, 期望保留原认证头", got)
	}
	// 普通自定义头可以正常写入。
	if got := req.Headers.Get("X-Custom-Tag"); got != "octopus" {
		t.Fatalf("X-Custom-Tag = %q, 期望 octopus", got)
	}
}

func TestApplyChannelRequestOptionsSetsSensitiveHeaderWhenAbsent(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{
		CustomHeader: []dbmodel.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: "Bearer from-custom"},
		},
	})

	req := &httpclient.Request{Headers: http.Header{}}
	ra.applyChannelRequestOptions(req)

	// 上游请求尚未带认证头时，敏感的自定义头应当被写入。
	if got := req.Headers.Get("Authorization"); got != "Bearer from-custom" {
		t.Fatalf("Authorization = %q, 期望写入自定义认证头", got)
	}
}

func TestMiddlewareOnOutboundRawErrorCapturesStatus(t *testing.T) {
	m := &relayPipelineMiddleware{attempt: newTestAttempt(&dbmodel.Channel{})}
	m.OnOutboundRawError(context.Background(), &httpclient.Error{StatusCode: 429, Status: "429 Too Many Requests"})
	if m.upstreamStatusCode != 429 {
		t.Fatalf("upstreamStatusCode = %d, 期望 429", m.upstreamStatusCode)
	}
}

func TestMiddlewareOnOutboundRawErrorIgnoresNonHTTPError(t *testing.T) {
	m := &relayPipelineMiddleware{attempt: newTestAttempt(&dbmodel.Channel{})}
	m.OnOutboundRawError(context.Background(), errors.New("dial timeout"))
	// 非 httpclient.Error 的传输错误没有上游状态码，应保持 0 让调度走默认失败路径。
	if m.upstreamStatusCode != 0 {
		t.Fatalf("upstreamStatusCode = %d, 非 HTTP 错误期望为 0", m.upstreamStatusCode)
	}
}

func TestMiddlewareOnOutboundLlmResponseRecordsUsage(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{})
	m := &relayPipelineMiddleware{attempt: ra}

	resp := &llm.Response{Usage: &llm.Usage{PromptTokens: 12, CompletionTokens: 34}}
	got, err := m.OnOutboundLlmResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if got != resp {
		t.Fatal("响应应原样透传")
	}
	if ra.metrics.Stats.InputToken != 12 || ra.metrics.Stats.OutputToken != 34 {
		t.Fatalf("usage 未记录: input=%d output=%d", ra.metrics.Stats.InputToken, ra.metrics.Stats.OutputToken)
	}
}

func TestMiddlewareOnOutboundLlmResponseNilSafe(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{})
	m := &relayPipelineMiddleware{attempt: ra}
	if _, err := m.OnOutboundLlmResponse(context.Background(), nil); err != nil {
		t.Fatalf("nil 响应不应报错: %v", err)
	}
	if ra.metrics.Stats.InputToken != 0 {
		t.Fatal("nil 响应不应记录 token")
	}
}

func TestMiddlewareOnOutboundRawRequestInitializesHeaders(t *testing.T) {
	m := &relayPipelineMiddleware{attempt: newTestAttempt(&dbmodel.Channel{})}
	got, err := m.OnOutboundRawRequest(context.Background(), &httpclient.Request{})
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if got.Headers == nil {
		t.Fatal("Headers 未初始化")
	}
}

func TestMiddlewareName(t *testing.T) {
	m := &relayPipelineMiddleware{}
	if m.Name() != "octopus_relay" {
		t.Fatalf("Name() = %q, 期望 octopus_relay", m.Name())
	}
}

func TestCompactGroupItemRankPlacesIncompatibleLast(t *testing.T) {
	channel := &dbmodel.Channel{Type: llm.APIFormatOpenAIResponse}
	usable := dbmodel.GroupItem{CompactStrategy: dbmodel.CompactStrategyChatManual}
	unknown := dbmodel.GroupItem{}
	incompatible := dbmodel.GroupItem{CompactStrategy: dbmodel.CompactStrategyIncompatible}

	if got, want := compactGroupItemRank(usable, nil), 2; got != want {
		t.Fatalf("chat_manual rank = %d, want %d", got, want)
	}
	if got, want := compactGroupItemRank(unknown, channel), 3; got != want {
		t.Fatalf("unknown OpenAI rank = %d, want %d", got, want)
	}
	if got := compactGroupItemRank(incompatible, nil); got <= compactGroupItemRank(unknown, nil) || got <= compactGroupItemRank(usable, nil) {
		t.Fatalf("incompatible rank = %d, want lower priority than unknown/useful ranks", got)
	}
}

func TestCompactCandidateOrderingPlacesIncompatibleAfterUsable(t *testing.T) {
	group := dbmodel.Group{
		Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{
			{ID: 1, ChannelID: 1, ModelName: "incompatible", Priority: 1, CompactStrategy: dbmodel.CompactStrategyIncompatible},
			{ID: 2, ChannelID: 2, ModelName: "official", Priority: 99, CompactStrategy: dbmodel.CompactStrategyOfficial},
		},
	}
	ranks := map[int]int{
		1: compactGroupItemRank(group.Items[0], nil),
		2: compactGroupItemRank(group.Items[1], nil),
	}

	iter := balancer.NewIteratorWithCandidateRanks(group, 0, "octopus-model", ranks)
	if !iter.Next() {
		t.Fatal("Next() = false, want first candidate")
	}
	if got := iter.Item().ID; got != 2 {
		t.Fatalf("first candidate ID = %d, want 2", got)
	}
	if !iter.Next() {
		t.Fatal("Next() = false, want second candidate")
	}
	if got := iter.Item().ID; got != 1 {
		t.Fatalf("second candidate ID = %d, want 1", got)
	}
}

func TestCompactStrategyOrderTreatsIncompatibleAsUncached(t *testing.T) {
	got := compactStrategyOrder(llm.APIFormatOpenAIResponse, compactStrategyIncompatible, true)
	want := []compactStrategy{compactStrategyOfficial, compactStrategyResponsesManual, compactStrategyChatManual}
	if len(got) != len(want) {
		t.Fatalf("order length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}

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

func TestFinalAttemptEmpty(t *testing.T) {
	id, name, keyID, status := finalAttempt(nil)
	if id != 0 || name != "" || keyID != 0 || status != "" {
		t.Fatalf("空尝试应返回零值, got id=%d name=%q key=%d status=%q", id, name, keyID, status)
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
		{Data: []byte(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`)},
	}
	body, meta, err := inbound.AggregateStreamChunks(context.Background(), events)
	if err != nil {
		t.Fatalf("聚合错误: %v", err)
	}

	m := &RelayMetrics{ActualModel: "no-price-model"}
	m.InternalResponse = body
	m.RecordUsage(meta.Usage)

	if m.Stats.InputToken != 9 || m.Stats.OutputToken != 3 {
		t.Fatalf("metrics usage = in:%d out:%d, 期望 9/3", m.Stats.InputToken, m.Stats.OutputToken)
	}
	if len(m.InternalResponse) == 0 {
		t.Fatal("InternalResponse 应保存聚合后的响应体")
	}
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

func TestApplyRawPassthroughUsesRawBodyAndPatchesActualModel(t *testing.T) {
	rawBody := `{"model":"client-model","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	internalRequest := &llm.Request{
		Model:      "actual-upstream-model",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	ra := newPassthroughAttempt(&dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion}, internalRequest)

	// outbound transformer 重序列化后的 body：字段顺序变化、cache_control 丢失。
	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"actual-upstream-model"}`)
	ra.applyChannelRequestOptions(req)

	want := `{"model":"actual-upstream-model","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	if string(req.Body) != want {
		t.Fatalf("raw passthrough body = %s\n期望 = %s", req.Body, want)
	}
	if !bytes.Equal(internalRequest.RawRequest.Body, []byte(rawBody)) {
		t.Fatalf("原始 RawRequest.Body 被修改: %s", internalRequest.RawRequest.Body)
	}
}

func TestApplyRawPassthroughUsesRawBodyWhenModelMatches(t *testing.T) {
	// 实际上游模型与原始请求模型相同：PatchModel 会返回 false，但仍必须使用 raw body。
	rawBody := `{"model":"same-model","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	internalRequest := &llm.Request{
		Model:      "same-model",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	ra := newPassthroughAttempt(&dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion}, internalRequest)

	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"same-model"}`)
	ra.applyChannelRequestOptions(req)

	if string(req.Body) != rawBody {
		t.Fatalf("same-model 时未透传 raw body\ngot:  %s\nwant: %s", req.Body, rawBody)
	}
}

func TestApplyRawPassthroughEnsuresStreamUsage(t *testing.T) {
	rawBody := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	internalRequest := &llm.Request{
		Model:      "m",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		Stream:     boolPtr(true),
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	ra := newPassthroughAttempt(&dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion}, internalRequest)

	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"m","stream":true}`)
	ra.applyChannelRequestOptions(req)

	if !strings.Contains(string(req.Body), `"stream_options":{"include_usage":true}`) {
		t.Fatalf("流式 raw passthrough 未补 include_usage: %s", req.Body)
	}
}

func TestApplyRawPassthroughFallsBackWhenIneligible(t *testing.T) {
	rawBody := `{"model":"client-model","messages":[{"role":"user","content":"hi"}]}`
	transformed := `{"messages":[{"role":"user","content":"hi"}],"model":"actual-model"}`

	cases := []struct {
		name      string
		channel   *dbmodel.Channel
		mutate    func(*llm.Request)
		mutateReq func(*httpclient.Request)
	}{
		{
			name:    "开关关闭",
			channel: &dbmodel.Channel{RawPassthrough: false, Type: llm.APIFormatOpenAIChatCompletion},
		},
		{
			name:    "出站非 OpenAI Chat",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatAnthropicMessage},
		},
		{
			name:    "入站非 OpenAI Chat",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion},
			mutate:  func(r *llm.Request) { r.APIFormat = llm.APIFormatAnthropicMessage },
		},
		{
			name:    "缺失 RawRequest",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion},
			mutate:  func(r *llm.Request) { r.RawRequest = nil },
		},
		{
			name:    "非 JSON 出站",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion},
			mutateReq: func(req *httpclient.Request) {
				req.Headers = http.Header{"Content-Type": {"multipart/form-data"}}
				req.ContentType = "multipart/form-data"
			},
		},
		{
			name:    "raw body 缺少顶层 string model",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion},
			mutate:  func(r *llm.Request) { r.RawRequest.Body = []byte(`{"messages":[]}`) },
		},
		{
			name:    "raw body 顶层重复 model 触发回退",
			channel: &dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion},
			// 顶层重复 model 存在路由绕过风险，必须回退常规转换路径而非透传原始 body。
			mutate: func(r *llm.Request) {
				r.RawRequest.Body = []byte(`{"model":"actual-model","model":"evil-model","messages":[]}`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			internalRequest := &llm.Request{
				Model:      "actual-model",
				APIFormat:  llm.APIFormatOpenAIChatCompletion,
				RawRequest: &httpclient.Request{Body: []byte(rawBody)},
			}
			if tc.mutate != nil {
				tc.mutate(internalRequest)
			}
			ra := newPassthroughAttempt(tc.channel, internalRequest)
			req := jsonRequest(transformed)
			if tc.mutateReq != nil {
				tc.mutateReq(req)
			}
			ra.applyChannelRequestOptions(req)

			if string(req.Body) != transformed {
				t.Fatalf("不满足触发条件时应回退常规 body\ngot:  %s\nwant: %s", req.Body, transformed)
			}
		})
	}
}

func TestApplyRawPassthroughThenParamOverrideThenCustomHeader(t *testing.T) {
	rawBody := `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":1}`
	internalRequest := &llm.Request{
		Model:      "m",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	override := `{"temperature":0.2}`
	ra := newPassthroughAttempt(&dbmodel.Channel{
		RawPassthrough: true,
		Type:           llm.APIFormatOpenAIChatCompletion,
		ParamOverride:  &override,
		CustomHeader: []dbmodel.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: "Bearer should-not-win"},
			{HeaderKey: "X-Tag", HeaderValue: "octopus"},
		},
	}, internalRequest)

	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"m","temperature":1}`)
	req.Headers.Set("Authorization", "Bearer original")
	ra.applyChannelRequestOptions(req)

	// raw 先生效，ParamOverride 在其上覆盖 temperature。
	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("body 不是合法 JSON: %v", err)
	}
	if got["temperature"] != 0.2 {
		t.Fatalf("temperature = %v, 期望被 ParamOverride 覆盖为 0.2", got["temperature"])
	}
	// 敏感认证头保持原值，普通自定义头写入。
	if req.Headers.Get("Authorization") != "Bearer original" {
		t.Fatalf("Authorization = %q, 期望保留原认证头", req.Headers.Get("Authorization"))
	}
	if req.Headers.Get("X-Tag") != "octopus" {
		t.Fatalf("X-Tag = %q, 期望 octopus", req.Headers.Get("X-Tag"))
	}
}

// TestFirstTokenGuardStopBeforeTimeoutKeepsContextAlive 验证 M3 竞态修复：
// 首个 token 到达（stop）后，即便等过原超时窗口，context 也不能被以首字超时原因取消。
func TestFirstTokenGuardStopBeforeTimeoutKeepsContextAlive(t *testing.T) {
	ctx, stop, release := newFirstTokenGuard(context.Background(), 20*time.Millisecond)
	defer release()

	// 模拟首个 token 在超时前到达。
	stop()
	// 等待超过原超时窗口，确认计时器即使触发也不会取消 context。
	time.Sleep(60 * time.Millisecond)

	select {
	case <-ctx.Done():
		t.Fatalf("stop 后 context 不应被取消: cause=%v", context.Cause(ctx))
	default:
	}
}

// TestFirstTokenGuardTimeoutCancelsWithCause 验证首 token 未到达时，超时以 errFirstTokenTimeout 取消。
func TestFirstTokenGuardTimeoutCancelsWithCause(t *testing.T) {
	ctx, _, release := newFirstTokenGuard(context.Background(), 10*time.Millisecond)
	defer release()

	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
			t.Fatalf("取消原因 = %v, 期望 errFirstTokenTimeout", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("超时应触发 context 取消")
	}
}

// TestFirstTokenGuardStopAfterTimeoutStaysTimedOut 验证竞态另一侧：
// 若计时器已先取胜取消，随后到来的 stop 不得把已超时的尝试翻转成功（取消原因保持不变）。
func TestFirstTokenGuardStopAfterTimeoutStaysTimedOut(t *testing.T) {
	ctx, stop, release := newFirstTokenGuard(context.Background(), 10*time.Millisecond)
	defer release()

	<-ctx.Done()
	// 计时器已取消后才调用 stop（首 token 实际未及时到达）。
	stop()

	if !errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
		t.Fatalf("取消原因 = %v, 期望保持 errFirstTokenTimeout", context.Cause(ctx))
	}
}

// TestFirstTokenGuardReleaseUsesNonTimeoutCause 验证正常结束 release 后，
// context 取消原因不是首字超时（用于和误切通道路径区分）。
func TestFirstTokenGuardReleaseUsesNonTimeoutCause(t *testing.T) {
	ctx, stop, release := newFirstTokenGuard(context.Background(), time.Hour)
	stop()
	release()

	<-ctx.Done()
	if errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
		t.Fatalf("正常结束不应以首字超时原因取消: cause=%v", context.Cause(ctx))
	}
}

// TestApplyRawPassthroughRecordsOutboundSummary 验证 M6：raw passthrough 生效时记录最终出站摘要。
func TestApplyRawPassthroughRecordsOutboundSummary(t *testing.T) {
	rawBody := `{"model":"client-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	internalRequest := &llm.Request{
		Model:      "actual-model",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		Stream:     boolPtr(true),
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	ra := newPassthroughAttempt(&dbmodel.Channel{RawPassthrough: true, Type: llm.APIFormatOpenAIChatCompletion}, internalRequest)

	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"actual-model","stream":true}`)
	ra.applyChannelRequestOptions(req)

	s := ra.metrics.OutboundRequestSummary
	if s == nil {
		t.Fatal("raw passthrough 生效时应记录出站摘要")
	}
	if !s.RawPassthrough {
		t.Fatal("摘要应标记 raw_passthrough=true")
	}
	if s.Model != "actual-model" {
		t.Fatalf("摘要 model = %q, 期望 actual-model（实际上游模型）", s.Model)
	}
	if s.Stream == nil || !*s.Stream {
		t.Fatalf("摘要 stream 应为 true, got %v", s.Stream)
	}
	if s.StreamOptions == nil || s.StreamOptions["include_usage"] != true {
		t.Fatalf("摘要应反映补入的 stream_options.include_usage: %+v", s.StreamOptions)
	}
	if s.BodyBytes != len(req.Body) {
		t.Fatalf("摘要 body_bytes = %d, 期望 %d", s.BodyBytes, len(req.Body))
	}
	if len(s.BodySHA256) != 64 {
		t.Fatalf("摘要 body_sha256 长度 = %d, 期望 64", len(s.BodySHA256))
	}
}

// TestApplyRawPassthroughSummaryMarksParamOverride 验证摘要标记 ParamOverride 已叠加。
func TestApplyRawPassthroughSummaryMarksParamOverride(t *testing.T) {
	rawBody := `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":1}`
	internalRequest := &llm.Request{
		Model:      "m",
		APIFormat:  llm.APIFormatOpenAIChatCompletion,
		RawRequest: &httpclient.Request{Body: []byte(rawBody)},
	}
	override := `{"temperature":0.2}`
	ra := newPassthroughAttempt(&dbmodel.Channel{
		RawPassthrough: true,
		Type:           llm.APIFormatOpenAIChatCompletion,
		ParamOverride:  &override,
	}, internalRequest)

	req := jsonRequest(`{"messages":[{"role":"user","content":"hi"}],"model":"m","temperature":1}`)
	ra.applyChannelRequestOptions(req)

	s := ra.metrics.OutboundRequestSummary
	if s == nil || !s.ParamOverrideApplied {
		t.Fatalf("摘要应标记 param_override_applied=true, got %+v", s)
	}
}

// TestApplyChannelRequestOptionsNoSummaryWhenNotPassthrough 验证非透传渠道不产生出站摘要，行为不变。
func TestApplyChannelRequestOptionsNoSummaryWhenNotPassthrough(t *testing.T) {
	override := `{"temperature":0.5}`
	ra := newTestAttempt(&dbmodel.Channel{ParamOverride: &override})
	req := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"model":"m","temperature":1}`),
	}
	ra.applyChannelRequestOptions(req)

	if ra.metrics.OutboundRequestSummary != nil {
		t.Fatal("非 raw passthrough 渠道不应产生出站摘要")
	}
}

// TestRequestContentEmbedsOutboundSummary 验证 requestContent 在有摘要时并入 _outbound_request。
func TestRequestContentEmbedsOutboundSummary(t *testing.T) {
	m := &RelayMetrics{
		ActualModel:     "actual-model",
		InternalRequest: &llm.Request{Model: "client-model"},
		OutboundRequestSummary: &OutboundRequestSummary{
			RawPassthrough: true,
			Model:          "actual-model",
			BodyBytes:      42,
			BodySHA256:     "abc",
		},
	}
	content := m.requestContent()

	var got map[string]any
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("requestContent 不是合法 JSON: %v", err)
	}
	outbound, ok := got["_outbound_request"].(map[string]any)
	if !ok {
		t.Fatalf("requestContent 缺少 _outbound_request: %s", content)
	}
	if outbound["raw_passthrough"] != true {
		t.Fatalf("_outbound_request.raw_passthrough = %v, 期望 true", outbound["raw_passthrough"])
	}
	if outbound["model"] != "actual-model" {
		t.Fatalf("_outbound_request.model = %v, 期望 actual-model", outbound["model"])
	}
}

// TestRequestContentNoSummaryKeyWhenAbsent 验证无摘要时不写入 _outbound_request，保持原行为。
func TestRequestContentNoSummaryKeyWhenAbsent(t *testing.T) {
	m := &RelayMetrics{
		ActualModel:     "m",
		InternalRequest: &llm.Request{Model: "m"},
	}
	content := m.requestContent()

	var got map[string]any
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("requestContent 不是合法 JSON: %v", err)
	}
	if _, exists := got["_outbound_request"]; exists {
		t.Fatalf("无摘要时不应写入 _outbound_request: %s", content)
	}
}
