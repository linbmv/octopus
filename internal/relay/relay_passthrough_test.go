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
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

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
	rawBody := `{"model":"client-model","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false,"private":"must-not-enter-summary"}}`
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
	if _, exists := s.StreamOptions["private"]; exists {
		t.Fatalf("摘要泄露了任意 stream_options 内容: %+v", s.StreamOptions)
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
	if !s.RequestRewriteApplied {
		t.Fatalf("摘要应标记 request_rewrite_applied=true, got %+v", s)
	}
}

// TestApplyChannelRequestOptionsSummarizesNonPassthroughRewrite verifies that
// advanced/legacy rewrites remain auditable without retaining body contents.
func TestApplyChannelRequestOptionsSummarizesNonPassthroughRewrite(t *testing.T) {
	override := `{"temperature":0.5}`
	ra := newTestAttempt(&dbmodel.Channel{ParamOverride: &override})
	req := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"model":"m","temperature":1}`),
	}
	ra.applyChannelRequestOptions(req)

	summary := ra.metrics.OutboundRequestSummary
	if summary == nil || summary.RawPassthrough || !summary.ParamOverrideApplied || !summary.RequestRewriteApplied {
		t.Fatalf("非透传参数覆盖摘要 = %+v", summary)
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
