package relay

import (
	"errors"
	"net/http"
	"strings"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

type ErrorAction string

const (
	ErrorActionNone         ErrorAction = "none"
	ErrorActionReturnClient ErrorAction = "return_client"
	ErrorActionRetryKey     ErrorAction = "retry_key"
	ErrorActionRetryChannel ErrorAction = "retry_channel"
)

type CompactCompatibilityAction string

const (
	CompactCompatibilityNone             CompactCompatibilityAction = "none"
	CompactCompatibilityMarkIncompatible CompactCompatibilityAction = "mark_incompatible"
)

// ErrorDecision is the relay-facing result of classifying an upstream failure.
// It keeps retry decisions next to classification so relay attempt code does
// not spread errorclass-specific checks across multiple branches.
type ErrorDecision struct {
	Classification   errorclass.Classification
	Action           ErrorAction
	ClientStatusCode int
	CompactAction    CompactCompatibilityAction

	// RetryNextKey is kept for callers/tests compiled against the first
	// ErrorDecision shape. New code should branch on Action.
	RetryNextKey bool
}

type errorDecisionOptions struct {
	OfficialCompactEndpoint bool
	PolicyProfile           dbmodel.ChannelPolicyProfile
}

type terminalRelayError struct {
	err        error
	statusCode int
}

func (e *terminalRelayError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *terminalRelayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *terminalRelayError) StatusCode() int {
	if e == nil || e.statusCode == 0 {
		return 502
	}
	return e.statusCode
}

func newTerminalRelayError(statusCode int, err error) error {
	return &terminalRelayError{statusCode: statusCode, err: err}
}

type classifiedClientRelayError struct {
	cause      error
	reason     string
	statusCode int
}

func (e *classifiedClientRelayError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "upstream rejected request"
	}
	return e.reason
}

func (e *classifiedClientRelayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *classifiedClientRelayError) StatusCode() int {
	if e == nil || e.statusCode < 400 || e.statusCode > 499 {
		return http.StatusBadRequest
	}
	return e.statusCode
}

func decideRelayError(statusCode int, headers http.Header, responseBody []byte, err error) ErrorDecision {
	return decideRelayErrorWithOptions(statusCode, headers, responseBody, err, errorDecisionOptions{})
}

func decideRelayErrorWithOptions(statusCode int, headers http.Header, responseBody []byte, err error, options errorDecisionOptions) ErrorDecision {
	contentType := ""
	if headers != nil {
		contentType = headers.Get("Content-Type")
	}
	classification := errorclass.ClassifyResponse(statusCode, headers, responseBody, contentType)
	if isFirstTokenTimeoutError(err) {
		reason := "first token timeout"
		var timeoutErr *firstTokenTimeoutError
		if errors.As(err, &timeoutErr) && timeoutErr.config.Source == firstTokenTimeoutNonStreamAttempt {
			reason = "non-stream attempt timeout"
		}
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: reason,
		}
	} else if errors.Is(err, errNonStreamRequestTimeout) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "non-streaming request timeout",
		}
	} else if errors.Is(err, errStreamIdleTimeout) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "stream idle timeout",
		}
	} else if errors.Is(err, bodylimit.ErrTooLarge) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "upstream response body too large",
		}
	} else if isEmptyUpstreamResponseError(err) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "empty upstream response",
		}
	} else if err != nil && classification.Level == errorclass.ErrorLevelNone {
		// Network failures, malformed/soft 2xx responses, and other failures
		// without an HTTP error status are properties of the selected upstream,
		// not of a client key. Never persist a failed attempt with level=none.
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "upstream transport or response failure",
		}
	}
	decision := decisionForClassification(statusCode, classification)
	applyClientErrorRetryPolicy(&decision, options.PolicyProfile)
	if options.OfficialCompactEndpoint && isCompactEndpointUnsupported(statusCode, responseBody, err) {
		decision.Classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "official compact endpoint is incompatible",
		}
		decision.Action = ErrorActionRetryChannel
		decision.ClientStatusCode = 0
		decision.CompactAction = CompactCompatibilityMarkIncompatible
		decision.RetryNextKey = false
	}
	return decision
}

func decisionForClassification(statusCode int, classification errorclass.Classification) ErrorDecision {
	decision := ErrorDecision{
		Classification: classification,
		Action:         ErrorActionNone,
		CompactAction:  CompactCompatibilityNone,
	}
	switch classification.Level {
	case errorclass.ErrorLevelClient:
		decision.Action = ErrorActionReturnClient
		decision.ClientStatusCode = statusCode
		if decision.ClientStatusCode < 400 || decision.ClientStatusCode > 499 {
			decision.ClientStatusCode = http.StatusBadRequest
		}
	case errorclass.ErrorLevelKey:
		decision.Action = ErrorActionRetryKey
		decision.RetryNextKey = true
	case errorclass.ErrorLevelChannel:
		decision.Action = ErrorActionRetryChannel
	}
	return decision
}

// applyClientErrorRetryPolicy 把"歧义 client"错误降级为可跨渠道探测，同时保留
// Level=client 的健康语义（不扣渠道健康分、不冷却 URL、不熔断）。
//
// 背景：上游代理网关（new-api / one-api 及其分叉）经常用 400 / invalid_request_error /
// 404 model_not_found 传递本应是 5xx/401 的错误。当分类器无法明确判定为"用户
// payload 错"时，reason 会保留 "400 bad request" / "upstream invalid_request_error"
// 等歧义字样；这类错误换个渠道就可能成功，不应中断 failover。
//
// 明确的用户 payload 错（context length、tool schema、prompt too long 等）会
// 在 classifier 阶段带上 "deterministic" / 具体词标记，此处保持 ReturnClient 立即返回。
//
// profile 参数暂时保留以兼容既有调用点；生产 35 个渠道均为 standard，历史的
// proxyProfile 分支不再是唯一入口，逻辑改为按 reason 精细判定。
func applyClientErrorRetryPolicy(decision *ErrorDecision, _ dbmodel.ChannelPolicyProfile) {
	if decision == nil || decision.Classification.Level != errorclass.ErrorLevelClient {
		return
	}
	reason := strings.ToLower(strings.TrimSpace(decision.Classification.Reason))
	if isDeterministicClientError(reason) {
		return // keep Action=ReturnClient — payload itself is invalid, changing channel cannot help
	}
	// Ambiguous client error: allow cross-channel probing.
	// Level stays client so health stats/URL cooldown/circuit breaker skip it;
	// Action changes to retry_channel so runner.iter advances to the next candidate.
	decision.Action = ErrorActionRetryChannel
}

// isDeterministicClientError identifies reasons that reflect a genuine client
// payload problem (invalid tool schema, context length exceeded, malformed
// input). These must NOT be retried across channels because no other upstream
// can satisfy them either.
func isDeterministicClientError(reason string) bool {
	return strings.Contains(reason, "deterministic client") ||
		strings.Contains(reason, "context length") ||
		strings.Contains(reason, "context window") ||
		strings.Contains(reason, "prompt is too long") ||
		strings.Contains(reason, "prompt too long") ||
		strings.Contains(reason, "maximum context") ||
		strings.Contains(reason, "maximum number of tokens") ||
		strings.Contains(reason, "too many tokens") ||
		strings.Contains(reason, "tool schema") ||
		strings.Contains(reason, "invalid tool") ||
		strings.Contains(reason, "invalid function") ||
		strings.Contains(reason, "invalid content")
	// NOTE: "tool call state mismatch" is intentionally NOT deterministic — it
	// reflects upstream tool_call_id tracking differences (new-api / one-api
	// variants), and a different provider often accepts the same payload. Keep
	// it in the ambiguous bucket so runer can try the next candidate.
}

func (ra *relayAttempt) decideError(statusCode int, headers http.Header, responseBody []byte, err error) ErrorDecision {
	officialCompact := ra != nil && ra.relayRun != nil && ra.channel != nil &&
		isCompactOpenAIChannel(ra.channel.Type, ra.internalRequest)
	profile := dbmodel.ChannelPolicyStandard
	if ra != nil && ra.channel != nil && ra.channel.PolicyProfile != "" {
		profile = ra.channel.PolicyProfile
	}
	return decideRelayErrorWithOptions(statusCode, headers, responseBody, err, errorDecisionOptions{
		OfficialCompactEndpoint: officialCompact,
		PolicyProfile:           profile,
	})
}

// shouldRecordURLFailure 判定一次失败是否应给所选 base URL 记冷却。
// 只有通道级分类（网络故障、5xx、超时、软错误）才是端点状态的证据；
// key 级与 client 级失败换个 URL 也一样，不应影响 URL 优选。
func shouldRecordURLFailure(decision ErrorDecision) bool {
	return decision.Classification.Level == errorclass.ErrorLevelChannel
}

func runtimeFailurePenalties(decision ErrorDecision, err error, endpointFallbackPending bool) (recordURLFailure, recordHealthFailure bool) {
	// Eligible initial-response timeouts enter the separate passive slow-recovery
	// state. They remain request failures for audit purposes, but are not proof
	// of a broken endpoint and must not feed URL cooldown or the circuit breaker.
	nonPunitiveTimeout := isNonPunitiveFirstTokenTimeout(err) || isSlowRecoveryTimeout(err)
	recordURLFailure = shouldRecordURLFailure(decision) && !nonPunitiveTimeout
	recordHealthFailure = decision.Classification.Level != errorclass.ErrorLevelClient &&
		!nonPunitiveTimeout && !endpointFallbackPending
	return recordURLFailure, recordHealthFailure
}

// isEndpointUnsupportedError is retained as a compatibility wrapper for the
// focused endpoint tests. Compact routing uses isCompactEndpointUnsupported so
// it can also inspect the response body and avoid model_not_found false marks.
func isEndpointUnsupportedError(err error) bool {
	return isCompactEndpointUnsupported(0, nil, err)
}

func isCompactEndpointUnsupported(statusCode int, responseBody []byte, err error) bool {
	if err == nil && statusCode == 0 {
		return false
	}
	text := boundedErrorDecisionText(responseBody, err)
	if containsModelNotFoundMarker(text) {
		return false
	}
	for _, marker := range []string{
		"invalid url",
		"no such endpoint",
		"unknown endpoint",
		"route not found",
		"cannot post",
		"unsupported endpoint",
		"endpoint not found",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if statusCode == 0 {
		statusCode = upstreamErrorStatus(err)
	}
	switch statusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func boundedErrorDecisionText(responseBody []byte, err error) string {
	const maxDecisionTextBytes = 8192
	if len(responseBody) > maxDecisionTextBytes {
		responseBody = responseBody[:maxDecisionTextBytes]
	}
	var b strings.Builder
	b.Grow(len(responseBody) + 128)
	b.Write(responseBody)
	if err != nil {
		b.WriteByte(' ')
		message := err.Error()
		if len(message) > maxDecisionTextBytes {
			message = message[:maxDecisionTextBytes]
		}
		b.WriteString(message)
	}
	return strings.ToLower(b.String())
}

func containsModelNotFoundMarker(text string) bool {
	return strings.Contains(text, "model_not_found") ||
		strings.Contains(text, "model not found") ||
		(strings.Contains(text, "model") && strings.Contains(text, "does not exist"))
}

func upstreamErrorStatus(err error) int {
	var responseErr *llm.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		return responseErr.StatusCode
	}
	var upstreamErr *httpclient.Error
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return upstreamErr.StatusCode
	}
	return 0
}

func isEmptyUpstreamResponseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "response body is empty") ||
		strings.Contains(msg, "empty response detected") ||
		strings.Contains(msg, "empty pipeline result") ||
		strings.Contains(msg, "empty pipeline response") ||
		strings.Contains(msg, "empty pipeline stream")
}
