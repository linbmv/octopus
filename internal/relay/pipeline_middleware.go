package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/transformer"
)

// relayPipelineMiddleware 承接 octopus 自己的通道级副作用：
// 1. 在 pipeline 发出上游请求前应用渠道参数覆盖和自定义 header；
// 2. 在上游失败时保存 HTTP 状态码和响应体，供 key 冷却、熔断和后续选路使用；
// 3. 在非流式响应转成 llm.Response 后记录 usage。
// axonhub/llm 只提供了部分函数式 middleware 构造器，错误状态码和 llm 响应 usage 这两个回调没有公开构造器，
// 所以这里保留一个很薄的结构体实现完整接口，而不是在 relay 主流程里重复 pipeline 的执行逻辑。
type relayPipelineMiddleware struct {
	pipeline.DummyMiddleware
	attempt              *relayAttempt
	upstreamStatusCode   int
	upstreamHeaders      http.Header
	upstreamResponseBody []byte
}

func (m *relayPipelineMiddleware) Name() string {
	return "octopus_relay"
}

func (m *relayPipelineMiddleware) OnOutboundRawRequest(ctx context.Context, request *httpclient.Request) (*httpclient.Request, error) {
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	m.attempt.applyChannelRequestOptions(request)

	// Cross-format fallback: strip Codex/Responses-specific fields when falling back
	// from Responses to OpenAI Chat (e.g., nested gpt-5.5 group items under opus group).
	if m.attempt.needsCrossFormatCleaning() {
		if err := m.attempt.stripCodexFieldsFromRequestBody(request); err != nil {
			log.Warnf("failed to strip Codex fields for cross-format fallback: %v", err)
			// Non-fatal: let the request proceed; upstream will reject if fields remain.
		}
	}
	// AxonHub's common model intentionally covers the intersection of provider
	// formats. Restore file/input_file parts at the final wire boundary so a
	// Chat/Responses/Anthropic/Gemini conversion does not silently drop an
	// attachment that the source protocol can represent.
	if err := m.attempt.applyRelayAttachmentCompatibility(request); err != nil {
		log.Warnf("failed to preserve outbound attachments: %v", err)
	}
	return request, nil
}

func (m *relayPipelineMiddleware) OnOutboundRawError(ctx context.Context, err error) {
	var upstreamErr *httpclient.Error
	if errors.As(err, &upstreamErr) {
		// pipeline 会把上游错误转换成统一错误返回；这里在转换前记录原始 HTTP 状态码和响应体，用于渠道 key 的后续调度决策。
		m.upstreamStatusCode = upstreamErr.StatusCode
		m.upstreamHeaders = upstreamErr.Headers.Clone()
		// 保存响应体用于智能错误分类（如 503 + model_not_found）
		m.upstreamResponseBody = upstreamErr.Body
	}
}

// needsCrossFormatCleaning detects when inbound is Codex/Responses format but
// outbound channel is OpenAI Chat, requiring Codex-specific field removal.
func (ra *relayAttempt) needsCrossFormatCleaning() bool {
	if ra.internalRequest == nil || ra.channel == nil {
		return false
	}
	inboundFormat := ra.internalRequest.APIFormat
	outboundFormat := ra.channel.Type
	// Strip Codex fields when: inbound is Responses (Codex) but outbound is OpenAI Chat.
	return (inboundFormat == llm.APIFormatOpenAIResponse || inboundFormat == llm.APIFormatOpenAIResponseCompact) &&
		outboundFormat == llm.APIFormatOpenAIChatCompletion
}

// stripCodexFieldsFromRequestBody removes Codex/Responses-specific fields that
// OpenAI Chat endpoints reject (e.g., reasoning_effort). Modifies request.Body in place.
func (ra *relayAttempt) stripCodexFieldsFromRequestBody(request *httpclient.Request) error {
	if !strings.Contains(strings.ToLower(request.ContentType+" "+request.Headers.Get("Content-Type")), "application/json") {
		return nil // Not JSON, cannot clean.
	}

	var bodyMap map[string]any
	if err := json.Unmarshal(request.Body, &bodyMap); err != nil {
		return fmt.Errorf("unmarshal request body: %w", err)
	}

	// Remove Codex-specific fields known to cause "invalid codex request" errors.
	codexFields := []string{"reasoning_effort"}
	for _, field := range codexFields {
		delete(bodyMap, field)
	}

	cleaned, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	request.Body = cleaned
	return nil
}

func (m *relayPipelineMiddleware) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	if response != nil {
		// 非流式 usage 已由 outbound transformer 标准化到 llm.Response；流式 usage 在最终聚合时记录，避免重复计数。
		m.attempt.metrics.RecordUsage(response.Usage)
	}
	return response, nil
}

// parsedRequestInbound 让 pipeline 复用 relay 在选路前已经解析好的 llm.Request。
// 这样每次候选通道尝试只重新执行 outbound transform 和 HTTP 请求，不会重复读取或解析客户端 body。
type parsedRequestInbound struct {
	transformer.Inbound
	request *llm.Request
}

func (in *parsedRequestInbound) TransformRequest(ctx context.Context, request *httpclient.Request) (*llm.Request, error) {
	if in.request == nil {
		return nil, fmt.Errorf("missing parsed request")
	}
	// relay 已经为选路解析过请求；pipeline 入口复用该结果，避免每次通道尝试再次解析同一份 body。
	in.request.RawRequest = request
	return in.request, nil
}
