package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

type fakeStream struct {
	events []*httpclient.StreamEvent
	index  int
	closed atomic.Bool // writeStream 的 reader 协程与主流程都可能触发 Close，用原子量避免测试自身 race
}

func (s *fakeStream) Next() bool {
	if s.index >= len(s.events) {
		return false
	}
	s.index++
	return true
}

func (s *fakeStream) Current() *httpclient.StreamEvent {
	if s.index == 0 || s.index > len(s.events) {
		return nil
	}
	return s.events[s.index-1]
}

func (s *fakeStream) Err() error { return nil }

func (s *fakeStream) Close() error {
	s.closed.Store(true)
	return nil
}

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

func TestFirstTokenTimeoutManualSourceTakesPrecedence(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{ID: 1})
	ra.group = dbmodel.Group{FirstTokenTimeOut: 12}
	ra.usedKey = dbmodel.ChannelKey{ID: 10}

	got := ra.firstTokenTimeout()
	if got.Source != firstTokenTimeoutManual {
		t.Fatalf("timeout source = %v, want manual", got.Source)
	}
	if got.Duration != 12*time.Second {
		t.Fatalf("timeout duration = %v, want 12s", got.Duration)
	}
}

func TestFirstTokenTimeoutPriorityManualThenAdaptiveThenGlobal(t *testing.T) {
	tests := []struct {
		name             string
		manualSeconds    int
		adaptive         time.Duration
		hasAdaptive      bool
		globalSeconds    int
		coldStartSeconds int
		hasAlternative   bool
		wantSource       firstTokenTimeoutSource
		wantDuration     time.Duration
	}{
		{name: "manual wins", manualSeconds: 9, adaptive: 4 * time.Second, hasAdaptive: true, globalSeconds: 30, coldStartSeconds: 5, hasAlternative: true, wantSource: firstTokenTimeoutManual, wantDuration: 9 * time.Second},
		{name: "adaptive sample wins", adaptive: 4 * time.Second, hasAdaptive: true, globalSeconds: 30, coldStartSeconds: 5, hasAlternative: true, wantSource: firstTokenTimeoutAdaptive, wantDuration: 4 * time.Second},
		{name: "global fallback", globalSeconds: 30, wantSource: firstTokenTimeoutGlobal, wantDuration: 30 * time.Second},
		{name: "disabled", wantSource: firstTokenTimeoutDisabled},
		// 冷启动收敛：无手工值、无健康样本、仍有故障转移余地 → 用更短的冷启动上限。
		{name: "cold start with alternative", globalSeconds: 600, coldStartSeconds: 30, hasAlternative: true, wantSource: firstTokenTimeoutColdStart, wantDuration: 30 * time.Second},
		// 最后一个候选保留全局耐心，避免误杀唯一可用但首字慢的模型。
		{name: "cold start without alternative falls back to global", globalSeconds: 600, coldStartSeconds: 30, hasAlternative: false, wantSource: firstTokenTimeoutGlobal, wantDuration: 600 * time.Second},
		// 冷启动值不小于全局值时没有意义，直接用全局值。
		{name: "cold start not tighter than global ignored", globalSeconds: 20, coldStartSeconds: 30, hasAlternative: true, wantSource: firstTokenTimeoutGlobal, wantDuration: 20 * time.Second},
		// 有健康样本时冷启动不介入。
		{name: "adaptive beats cold start", adaptive: 8 * time.Second, hasAdaptive: true, globalSeconds: 600, coldStartSeconds: 30, hasAlternative: true, wantSource: firstTokenTimeoutAdaptive, wantDuration: 8 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectFirstTokenTimeout(test.manualSeconds, test.adaptive, test.hasAdaptive, test.globalSeconds, test.coldStartSeconds, test.hasAlternative)
			if got.Source != test.wantSource || got.Duration != test.wantDuration {
				t.Fatalf("timeout = %#v, want source=%v duration=%s", got, test.wantSource, test.wantDuration)
			}
		})
	}
}

func TestAdaptiveTimeoutClassificationUsesErrorSnapshot(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{ID: 1})
	err := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutAdaptive}.
		Error(firstTokenTimeoutPhaseStreamFirstEvent)
	// The current group/global/health state is intentionally unrelated. Source
	// classification must come from the timeout that actually fired.
	ra.group.FirstTokenTimeOut = 99
	if !ra.isAdaptiveFirstTokenTimeout(fmt.Errorf("forward failed: %w", err)) {
		t.Fatal("adaptive timeout source was recomputed from mutable current policy")
	}
	globalErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutGlobal}.
		Error(firstTokenTimeoutPhaseStreamFirstEvent)
	if ra.isAdaptiveFirstTokenTimeout(globalErr) {
		t.Fatal("global fallback timeout was misclassified as adaptive")
	}
}

func TestFirstTokenShadowObserverDoesNotCancelRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{}, 1)
	_, release := newFirstTokenShadowObserver(10*time.Millisecond, func() { fired <- struct{}{} })
	defer release()

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("shadow observer did not record elapsed threshold")
	}
	if ctx.Err() != nil {
		t.Fatalf("shadow observer canceled request context: %v", ctx.Err())
	}
}

func TestHealthPolicyDefaultsPreserveRecoveryProbeSettings(t *testing.T) {
	policy := currentHealthPolicy()
	if policy.RecoveryProbeEvery != 20 {
		t.Fatalf("RecoveryProbeEvery = %d, want 20", policy.RecoveryProbeEvery)
	}
	if policy.RecoveryProbeInterval != 5*time.Minute {
		t.Fatalf("RecoveryProbeInterval = %v, want 5m", policy.RecoveryProbeInterval)
	}
}

func TestShouldProbeUnhealthyCandidateEveryNthCheck(t *testing.T) {
	atomic.StoreUint64(&healthRecoveryProbeCounter, 0)
	healthLastRecoveryProbeUnix.Store(time.Now().Unix())
	defer atomic.StoreUint64(&healthRecoveryProbeCounter, 0)
	defer healthLastRecoveryProbeUnix.Store(0)

	probeEvery := healthRecoveryProbeEvery()
	for i := 1; i < probeEvery; i++ {
		if shouldProbeUnhealthyCandidate(0.25) {
			t.Fatalf("probe triggered at check %d, want only every %d", i, probeEvery)
		}
	}
	if !shouldProbeUnhealthyCandidate(0.25) {
		t.Fatalf("probe not triggered at check %d", probeEvery)
	}
	if shouldProbeUnhealthyCandidate(0.8) {
		t.Fatal("healthy candidate should not trigger recovery probe")
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

func TestApplyChannelRequestOptionsNeverSetsSensitiveHeader(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{
		CustomHeader: []dbmodel.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: "Bearer from-custom"},
		},
	})

	req := &httpclient.Request{Headers: http.Header{}}
	ra.applyChannelRequestOptions(req)

	// 即使上游请求尚未带认证头，渠道改写也不能注入认证凭据。
	if got := req.Headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, 期望保持为空", got)
	}
}

func TestApplyChannelRequestOptionsAppliesBoundedHeaderRules(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{HeaderRules: []dbmodel.HeaderRule{
		{Action: "set", HeaderKey: "X-Trace", HeaderValue: "first"},
		{Action: "append", HeaderKey: "X-Trace", HeaderValue: "second"},
		{Action: "remove", HeaderKey: "X-Remove"},
		{Action: "remove", HeaderKey: "Authorization"},
	}})
	req := &httpclient.Request{Headers: http.Header{
		"X-Remove":      {"present"},
		"Authorization": {"Bearer adapter-owned"},
	}}

	ra.applyChannelRequestOptions(req)

	if got := req.Headers.Values("X-Trace"); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("X-Trace values = %#v", got)
	}
	if got := req.Headers.Get("X-Remove"); got != "" {
		t.Fatalf("X-Remove = %q, want removed", got)
	}
	if got := req.Headers.Get("Authorization"); got != "Bearer adapter-owned" {
		t.Fatalf("Authorization = %q, protected header was changed", got)
	}
	if summary := ra.metrics.OutboundRequestSummary; summary == nil || !summary.HeaderRewriteApplied || !summary.RequestRewriteApplied {
		t.Fatalf("header rewrite summary = %+v", summary)
	}
}

func TestApplyChannelRequestOptionsAppliesJSONPointerOverrideAndRemove(t *testing.T) {
	value := `0.25`
	ra := newTestAttempt(&dbmodel.Channel{JSONRewriteRules: []dbmodel.JSONRewriteRule{
		{Action: "override", Path: "/options/temperature", Value: &value},
		{Action: "remove", Path: "/messages/0/internal"},
	}})
	req := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"options":{},"messages":[{"content":"hello","internal":true}]}`),
	}

	ra.applyChannelRequestOptions(req)

	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("decode rewritten body: %v", err)
	}
	if got["options"].(map[string]any)["temperature"] != 0.25 {
		t.Fatalf("options = %#v", got["options"])
	}
	message := got["messages"].([]any)[0].(map[string]any)
	if _, exists := message["internal"]; exists {
		t.Fatalf("message = %#v, internal should be removed", message)
	}
	if summary := ra.metrics.OutboundRequestSummary; summary == nil || !summary.JSONRewriteApplied || summary.ParamOverrideApplied || !summary.RequestRewriteApplied {
		t.Fatalf("JSON rewrite summary = %+v", summary)
	}
}

func TestMiddlewareOnOutboundRawErrorCapturesStatus(t *testing.T) {
	m := &relayPipelineMiddleware{attempt: newTestAttempt(&dbmodel.Channel{})}
	headers := make(http.Header)
	headers.Set("Retry-After", "120")
	headers.Set("X-RateLimit-Scope", "organization")
	m.OnOutboundRawError(context.Background(), &httpclient.Error{StatusCode: 429, Status: "429 Too Many Requests", Headers: headers})
	if m.upstreamStatusCode != 429 {
		t.Fatalf("upstreamStatusCode = %d, 期望 429", m.upstreamStatusCode)
	}
	if m.upstreamHeaders.Get("Retry-After") != "120" || m.upstreamHeaders.Get("X-RateLimit-Scope") != "organization" {
		t.Fatalf("upstream headers not captured: %v", m.upstreamHeaders)
	}
	headers.Set("Retry-After", "1")
	if m.upstreamHeaders.Get("Retry-After") != "120" {
		t.Fatal("captured upstream headers must not alias mutable error headers")
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

func TestForwardCarriesUpstreamHeadersIntoProductionClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.Header().Set("X-RateLimit-Scope", "organization")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()

	rawRequest := &httpclient.Request{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		Headers:     http.Header{"Content-Type": {"application/json"}},
		Body:        []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	}
	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	internalRequest, err := inbound.TransformRequest(context.Background(), rawRequest)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	internalRequest.RawRequest = rawRequest
	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "rate-limited",
		Type:     llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}},
	}
	outbound, err := newOutbound(channel.Type, internalRequest, channel.GetBaseUrl(), "test-key")
	if err != nil {
		t.Fatalf("new outbound: %v", err)
	}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	attempt := &relayAttempt{
		relayRun: &relayRun{
			c:               ginContext,
			inAdapter:       inbound,
			internalRequest: internalRequest,
			metrics:         &RelayMetrics{ActualModel: internalRequest.Model},
		},
		outAdapter: outbound,
		channel:    channel,
		usedKey:    dbmodel.ChannelKey{ID: 1, ChannelKey: "test-key"},
	}

	status, headers, body, err := attempt.forward()
	if err == nil || status != http.StatusTooManyRequests {
		t.Fatalf("forward = status %d, err %v; want 429 error", status, err)
	}
	if headers.Get("Retry-After") != "120" || headers.Get("X-RateLimit-Scope") != "organization" {
		t.Fatalf("forward headers = %v", headers)
	}
	decision := decideRelayError(status, headers, body, err)
	if decision.Classification.Level != errorclass.ErrorLevelChannel || decision.RetryNextKey {
		t.Fatalf("production decision = %#v, want channel without key retry", decision)
	}
}

func TestRelayAttemptCanRetryNextKeyOnlyForKeyLevelStatuses(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{})
	ra.c = testGinContext()
	ra.keyOptions = []dbmodel.ChannelKey{{ID: 1}, {ID: 2}}
	ra.usedKey = dbmodel.ChannelKey{ID: 1, StatusCode: http.StatusTooManyRequests}

	if !ra.canRetryNextKey(errors.New("rate limited"), nil, nil) {
		t.Fatal("429 should retry next key")
	}
	ra.usedKey.StatusCode = http.StatusForbidden
	if !ra.canRetryNextKey(errors.New("forbidden"), nil, nil) {
		t.Fatal("403 should retry next key")
	}
	if ra.canRetryNextKey(errors.New("client restricted"), nil, []byte(`{"code":"channel:client_restricted","error":"This channel does not allow the current client"}`)) {
		t.Fatal("403 client_restricted should not retry next key")
	}
	ra.usedKey.StatusCode = http.StatusBadGateway
	if ra.canRetryNextKey(errors.New("bad gateway"), nil, nil) {
		t.Fatal("502 should not retry next key")
	}

	// 503 + model_not_found 应该换 key（上游权限问题，不是真正的渠道故障）
	ra.usedKey.StatusCode = http.StatusServiceUnavailable
	if !ra.canRetryNextKey(errors.New("error: 分组 Gemini 下模型 deepseek/deepseek-v4-flash 无可用渠道（distributor）, code: model_not_found"), nil, []byte(`{"error":"model_not_found"}`)) {
		t.Fatal("503 + model_not_found should retry next key")
	}
	if !ra.canRetryNextKey(errors.New("Request failed: Service Unavailable, error: model not found"), nil, []byte(`{"error":"model not found"}`)) {
		t.Fatal("503 + 'model not found' should retry next key")
	}
	if !ra.canRetryNextKey(errors.New("invalid_model: the model is not supported"), nil, []byte(`{"error":"invalid_model"}`)) {
		t.Fatal("503 + invalid_model should retry next key")
	}

	// 503 但是真正的服务故障（如网络超时），不应该换 key
	if ra.canRetryNextKey(errors.New("upstream timeout"), nil, []byte(`{"error":"timeout"}`)) {
		t.Fatal("503 + generic error should not retry next key")
	}
}

func TestRelayErrorDecisionRetriesOnlyKeyLevelFailures(t *testing.T) {
	keyDecision := decideRelayError(http.StatusTooManyRequests, nil, nil, errors.New("upstream failed"))
	if !keyDecision.RetryNextKey {
		t.Fatal("429 key-level failure should retry next key")
	}

	timeoutDecision := decideRelayError(http.StatusOK, nil, nil, firstTokenTimeoutConfig{Duration: time.Second}.Error(firstTokenTimeoutPhaseWaitingHeaders))
	if timeoutDecision.RetryNextKey {
		t.Fatal("first token timeout should not retry next key")
	}
	if timeoutDecision.Classification.Level.String() != "channel" {
		t.Fatalf("first token timeout level = %s, want channel", timeoutDecision.Classification.Level)
	}
}

func TestRelayErrorDecisionUsesUpstreamRateLimitHeaders(t *testing.T) {
	decision := decideRelayError(
		http.StatusTooManyRequests,
		http.Header{"Retry-After": {"120"}},
		nil,
		errors.New("upstream rate limited"),
	)
	if decision.Classification.Level != errorclass.ErrorLevelChannel {
		t.Fatalf("classification = %s, want channel", decision.Classification.Level)
	}
	if decision.RetryNextKey {
		t.Fatal("long channel-scoped Retry-After must not rotate another key")
	}
}

func TestRelayErrorDecisionClassifiesTransportAndSoftResponseFailuresAsChannel(t *testing.T) {
	for _, status := range []int{0, http.StatusOK} {
		decision := decideRelayError(status, nil, nil, errors.New("upstream response failed"))
		if decision.Classification.Level != errorclass.ErrorLevelChannel || decision.RetryNextKey {
			t.Fatalf("status %d decision = %#v, want channel without key retry", status, decision)
		}
	}
}

func TestRelayErrorDecisionClassifiesEmptyUpstreamResponseAsChannel(t *testing.T) {
	decision := decideRelayError(http.StatusOK, nil, nil, errors.New("failed to stream request: response body is empty"))
	if decision.Classification.Level.String() != "channel" {
		t.Fatalf("empty response level = %s, want channel", decision.Classification.Level)
	}
	if decision.RetryNextKey {
		t.Fatal("empty channel response should fail over to another channel, not another key")
	}
}

func TestRelayErrorDecisionClassifiesNonStreamTimeoutAsChannel(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", errNonStreamRequestTimeout)
	decision := decideRelayError(0, nil, nil, err)
	if decision.Classification.Level != errorclass.ErrorLevelChannel {
		t.Fatalf("classification = %s, want channel", decision.Classification.Level)
	}
	if decision.RetryNextKey {
		t.Fatal("a request-wide timeout must not retry another key")
	}
}

func TestRelayErrorDecisionClassifiesStreamIdleAndOversizedResponseAsChannel(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", errStreamIdleTimeout),
		fmt.Errorf("wrapped: %w", &bodylimit.TooLargeError{Limit: 10}),
	} {
		decision := decideRelayError(http.StatusOK, nil, nil, err)
		if decision.Classification.Level != errorclass.ErrorLevelChannel || decision.RetryNextKey {
			t.Fatalf("decision for %v = %#v, want channel failure without key retry", err, decision)
		}
	}
}

func TestNewRelayRequestContextSeparatesStreamingAndNonStreamingBudgets(t *testing.T) {
	nonStreaming := &llm.Request{Model: "m"}
	ctx, cancel := newRelayRequestContext(context.Background(), nonStreaming, 1)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("non-streaming request should receive a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > time.Second {
		t.Fatalf("non-streaming deadline remaining = %s, want (0, 1s]", remaining)
	}

	stream := true
	streamCtx, streamCancel := newRelayRequestContext(context.Background(), &llm.Request{Model: "m", Stream: &stream}, 1)
	defer streamCancel()
	if _, ok := streamCtx.Deadline(); ok {
		t.Fatal("streaming request must not receive the non-streaming total deadline")
	}

	disabledCtx, disabledCancel := newRelayRequestContext(context.Background(), nonStreaming, 0)
	defer disabledCancel()
	if _, ok := disabledCtx.Deadline(); ok {
		t.Fatal("zero timeout should explicitly disable the deadline")
	}
}

func TestRelayErrorDecisionClassifiesClientErrorAsTerminal(t *testing.T) {
	decision := decideRelayError(http.StatusBadRequest, nil, []byte(`{"error":"invalid request"}`), errors.New("bad request"))
	if decision.Classification.Level.String() != "client" {
		t.Fatalf("400 error level = %s, want client", decision.Classification.Level)
	}
	if decision.RetryNextKey {
		t.Fatal("client error should not retry another key")
	}
}

func TestIsRequestContextCanceledRequiresCanceledRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !isRequestContextCanceled(ctx, context.Canceled) {
		t.Fatal("canceled request context should be treated as request cancellation")
	}
	if isRequestContextCanceled(context.Background(), context.Canceled) {
		t.Fatal("plain upstream context.Canceled without a canceled request context should not be treated as request cancellation")
	}
	if isRequestContextCanceled(ctx, nil) {
		t.Fatal("nil error should not be treated as request cancellation")
	}
}

func TestRelayAttemptSwitchToNextKeyRebuildsOutbound(t *testing.T) {
	ra := newTestAttempt(&dbmodel.Channel{
		Type: llm.APIFormatOpenAIChatCompletion,
	})
	ra.internalRequest = &llm.Request{RequestType: llm.RequestTypeChat}
	ra.baseURL = "https://example.com/v1"
	ra.keyOptions = []dbmodel.ChannelKey{
		{ID: 1, ChannelKey: "sk-1"},
		{ID: 2, ChannelKey: "sk-2", Remark: "fallback"},
	}
	ra.usedKey = ra.keyOptions[0]

	if !ra.switchToNextKey() {
		t.Fatal("switchToNextKey() = false, want true")
	}
	if ra.usedKey.ID != 2 {
		t.Fatalf("usedKey.ID = %d, want 2", ra.usedKey.ID)
	}
	if ra.outAdapter == nil {
		t.Fatal("outAdapter should be rebuilt")
	}
	if ra.keyRemark != "fallback" {
		t.Fatalf("keyRemark = %q, want fallback", ra.keyRemark)
	}
}

func testGinContext() *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return ctx
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

func TestMiddlewareCapturesRedactedFinalRequestArtifact(t *testing.T) {
	originalConfig := conf.Current()
	t.Cleanup(func() {
		if err := conf.Set(originalConfig); err != nil {
			t.Errorf("restore config: %v", err)
		}
	})
	enabled := originalConfig
	enabled.SelfHealing.Enabled = true
	if err := conf.Set(enabled); err != nil {
		t.Fatalf("enable self-healing: %v", err)
	}
	ra := newTestAttempt(&dbmodel.Channel{Type: llm.APIFormatOpenAIResponse})
	m := &relayPipelineMiddleware{attempt: ra}
	request := &httpclient.Request{
		Method: "POST",
		URL:    "https://provider.test/v1/responses?key=query-secret-value",
		Headers: http.Header{
			"Authorization":    {"Bearer secret"},
			"Content-Type":     {"application/json"},
			"User-Agent":       {"codex-tui/0.144.6"},
			"X-Private-Header": {"header-secret-value"},
		},
		ContentType: "application/json",
		Body:        []byte(`{"model":"test-model","input":[{"role":"user","content":"user-secret-prompt"}],"stream":true}`),
	}
	if _, err := m.OnOutboundRawRequest(context.Background(), request); err != nil {
		t.Fatalf("OnOutboundRawRequest error: %v", err)
	}
	artifact := ra.metrics.OutboundRequestArtifact
	if artifact == nil {
		t.Fatal("final request artifact was not captured")
		return
	}
	if artifact.Protocol != string(llm.APIFormatOpenAIResponse) || artifact.Model != "test-model" {
		t.Fatalf("artifact metadata = %#v", artifact)
	}
	if artifact.Headers["authorization"] != "[redacted]" || artifact.Headers["x-private-header"] != "[present]" {
		t.Fatalf("artifact headers = %#v", artifact.Headers)
	}
	if strings.Contains(artifact.URL, "query-secret-value") {
		t.Fatalf("artifact URL leaked query value: %q", artifact.URL)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	for _, secret := range []string{"Bearer secret", "header-secret-value", "user-secret-prompt", "query-secret-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("artifact retained secret %q: %s", secret, encoded)
		}
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
	official := dbmodel.GroupItem{CompactStrategy: dbmodel.CompactStrategyOfficial}
	unknown := dbmodel.GroupItem{}
	incompatible := dbmodel.GroupItem{CompactStrategy: dbmodel.CompactStrategyIncompatible}

	if got, want := compactGroupItemRank(official, nil), 0; got != want {
		t.Fatalf("official rank = %d, want %d", got, want)
	}
	if got, want := compactGroupItemRank(unknown, channel), 3; got != want {
		t.Fatalf("unknown OpenAI rank = %d, want %d", got, want)
	}
	if got := compactGroupItemRank(incompatible, nil); got <= compactGroupItemRank(unknown, nil) || got <= compactGroupItemRank(official, nil) {
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

func TestCandidateRanksFromTreeUsesBestNestedDescendant(t *testing.T) {
	groups := map[int]dbmodel.Group{
		2: {
			ID: 2,
			Items: []dbmodel.GroupItem{
				{ID: 21, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 3},
				{ID: 22, Type: dbmodel.GroupItemTypeChannel, ChannelID: 22},
			},
		},
		3: {
			ID: 3,
			Items: []dbmodel.GroupItem{
				{ID: 31, Type: dbmodel.GroupItemTypeChannel, ChannelID: 31},
			},
		},
	}
	resolve := func(id int, _ context.Context) (*dbmodel.Group, error) {
		group, ok := groups[id]
		if !ok {
			return nil, errors.New("group not found")
		}
		return &group, nil
	}
	candidates := []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 2, Priority: 1},
		{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, Priority: 2},
	}
	leafRanks := map[int]int{10: 1, 22: 2, 31: 0}
	ranks := candidateRanksFromTree(candidates, context.Background(), resolve, 1, func(item dbmodel.GroupItem) int {
		return leafRanks[item.ChannelID]
	})
	if ranks[1] != 0 || ranks[2] != 1 {
		t.Fatalf("ranks = %#v, want nested=0 direct=1", ranks)
	}

	group := dbmodel.Group{Mode: dbmodel.GroupModeFailover, Items: candidates}
	iter := balancer.NewIteratorFromCandidates(group, 0, "m", append([]dbmodel.GroupItem(nil), candidates...), ranks)
	if !iter.Next() || iter.Item().Type != dbmodel.GroupItemTypeGroup {
		t.Fatalf("first candidate = %+v, want nested group with supported descendant", iter.Item())
	}
}

func TestCompactNestedRankUsesOfficialDescendant(t *testing.T) {
	groups := map[int]dbmodel.Group{
		2: {
			ID: 2,
			Items: []dbmodel.GroupItem{
				{ID: 21, Type: dbmodel.GroupItemTypeChannel, ChannelID: 21, CompactStrategy: dbmodel.CompactStrategyOfficial},
			},
		},
	}
	resolve := func(id int, _ context.Context) (*dbmodel.Group, error) {
		group, ok := groups[id]
		if !ok {
			return nil, errors.New("group not found")
		}
		return &group, nil
	}
	candidates := []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeGroup, TargetGroupID: 2, Priority: 1},
		{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 10, Priority: 2},
	}
	ranks := candidateRanksFromTree(candidates, context.Background(), resolve, 5, func(item dbmodel.GroupItem) int {
		if item.CompactStrategy == dbmodel.CompactStrategyOfficial {
			return compactGroupItemRank(item, nil)
		}
		return 4
	})
	if ranks[1] != 0 || ranks[2] != 4 {
		t.Fatalf("compact ranks = %#v, want nested official=0 direct=4", ranks)
	}
}

func TestCompactStrategyOrderFollowsModelCanonicalOrder(t *testing.T) {
	got := compactStrategyOrder(llm.APIFormatOpenAIResponse)
	want := []compactStrategy{compactStrategy(dbmodel.CompactStrategyOfficial)}
	if len(got) != len(want) {
		t.Fatalf("order length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}
