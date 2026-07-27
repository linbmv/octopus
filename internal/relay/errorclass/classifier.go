package errorclass

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorLevel 表示错误的严重级别
type ErrorLevel int

const (
	// ErrorLevelNone 无错误（2xx 成功）
	ErrorLevelNone ErrorLevel = iota
	// ErrorLevelKey Key 级错误：应该冷却当前 Key，重试同渠道其他 Key
	ErrorLevelKey
	// ErrorLevelChannel 渠道级错误：应该冷却整个渠道，切换到其他渠道
	ErrorLevelChannel
	// ErrorLevelClient 客户端错误：不应该冷却，直接返回给客户端
	ErrorLevelClient
)

// String 返回错误级别的字符串表示
func (e ErrorLevel) String() string {
	switch e {
	case ErrorLevelNone:
		return "none"
	case ErrorLevelKey:
		return "key"
	case ErrorLevelChannel:
		return "channel"
	case ErrorLevelClient:
		return "client"
	default:
		return "unknown"
	}
}

// Classification 包含 HTTP 响应分类的结果
type Classification struct {
	Level  ErrorLevel
	Reason string // 分类原因（用于日志和调试）
}

// statusCodeMeta 状态码元数据
type statusCodeMeta struct {
	Level ErrorLevel
}

const maxErrorBodyScanBytes = 8192

// statusCodeMetaMap 状态码元数据映射表
// 设计原则：表驱动替代分散的 switch/map，提高可维护性
var statusCodeMetaMap = map[int]statusCodeMeta{
	// === 客户端取消 ===
	// 499: 上游返回的客户端关闭请求，应切换渠道重试
	// 注意：本地 context.Canceled 不走这里，由调用方处理
	499: {ErrorLevelChannel},

	// === Key 级错误：API Key 相关问题 ===
	http.StatusUnauthorized:    {ErrorLevelKey}, // 401 Unauthorized
	http.StatusPaymentRequired: {ErrorLevelKey}, // 402 Payment Required
	http.StatusForbidden:       {ErrorLevelKey}, // 403 Forbidden
	http.StatusTooManyRequests: {ErrorLevelKey}, // 429 Too Many Requests

	// === 渠道级错误：服务器端问题 ===
	444:                            {ErrorLevelChannel}, // nginx: No Response
	http.StatusInternalServerError: {ErrorLevelChannel}, // 500 Internal Server Error
	http.StatusBadGateway:          {ErrorLevelChannel}, // 502 Bad Gateway
	http.StatusServiceUnavailable:  {ErrorLevelChannel}, // 503 Service Unavailable
	http.StatusGatewayTimeout:      {ErrorLevelChannel}, // 504 Gateway Timeout
	520:                            {ErrorLevelChannel}, // Cloudflare: Unknown Error
	521:                            {ErrorLevelChannel}, // Cloudflare: Web Server Is Down
	524:                            {ErrorLevelChannel}, // Cloudflare: A Timeout Occurred

	// === 客户端错误：不冷却，直接返回 ===
	http.StatusBadRequest:                   {ErrorLevelClient},  // 400 Bad Request
	http.StatusMethodNotAllowed:             {ErrorLevelChannel}, // 405 Method Not Allowed（代理场景下是渠道配置问题）
	http.StatusNotAcceptable:                {ErrorLevelClient},  // 406 Not Acceptable
	http.StatusRequestTimeout:               {ErrorLevelClient},  // 408 Request Timeout
	http.StatusGone:                         {ErrorLevelClient},  // 410 Gone
	http.StatusRequestEntityTooLarge:        {ErrorLevelClient},  // 413 Payload Too Large
	http.StatusRequestURITooLong:            {ErrorLevelClient},  // 414 URI Too Long
	http.StatusUnsupportedMediaType:         {ErrorLevelClient},  // 415 Unsupported Media Type
	http.StatusRequestedRangeNotSatisfiable: {ErrorLevelClient},  // 416 Range Not Satisfiable
	http.StatusExpectationFailed:            {ErrorLevelClient},  // 417 Expectation Failed
}

// getStatusCodeMeta 获取状态码元数据
func getStatusCodeMeta(statusCode int) statusCodeMeta {
	if meta, ok := statusCodeMetaMap[statusCode]; ok {
		return meta
	}

	// 默认兜底策略
	if statusCode >= 500 {
		return statusCodeMeta{ErrorLevelChannel}
	}
	if statusCode >= 400 {
		// 未知 4xx 状态码默认 Key 级冷却（保守策略）
		return statusCodeMeta{ErrorLevelKey}
	}

	return statusCodeMeta{ErrorLevelNone}
}

// ClassifyWithHeaders 基于状态码 + headers + 响应体智能分类错误级别
// 支持高级场景：429 Retry-After、400 quota errors
// headers 使用 http.Header 类型以支持大小写无关的 header 查找
func ClassifyWithHeaders(statusCode int, headers http.Header, responseBody []byte) Classification {
	contentType := ""
	if headers != nil {
		contentType = headers.Get("Content-Type")
	}
	return ClassifyResponse(statusCode, headers, responseBody, contentType)
}

// ClassifyResponse is the single response classification entry point used by
// ordinary HTTP responses, HTTP-200 soft errors, and buffered SSE error events.
// For successful binary responses, a non-text content type disables body
// keyword scanning so arbitrary payload bytes cannot become routing signals.
func ClassifyResponse(statusCode int, headers http.Header, responseBody []byte, contentType string) Classification {
	if statusCode >= 200 && statusCode < 300 && isUnexpectedHTMLResponse(contentType, responseBody) {
		return Classification{Level: ErrorLevelChannel, Reason: "HTTP 2xx HTML/WAF response"}
	}
	// Structured quota and embedded/SSE errors can arrive behind HTTP 200, so
	// inspect the bounded body before the ordinary 2xx success fast path.
	if shouldInspectResponseBody(statusCode, contentType, responseBody) {
		if classification, ok := classifyEmbeddedError(statusCode, responseBody, allowsPlainTextMarkers(contentType)); ok {
			return classification
		}
	}

	// 2xx 成功
	if statusCode >= 200 && statusCode < 300 {
		return Classification{Level: ErrorLevelNone, Reason: "success"}
	}

	// 429 错误：根据 Retry-After header 判断限流范围
	if statusCode == http.StatusTooManyRequests {
		return classify429Error(headers, responseBody)
	}

	// 400 错误：根据响应体智能分类
	if statusCode == http.StatusBadRequest {
		return classify400Error(responseBody)
	}

	// 403 错误：根据响应体区分 key 权限与渠道/客户端限制
	if statusCode == http.StatusForbidden {
		return classify403Error(responseBody)
	}

	// 404 错误：根据响应体智能分类
	if statusCode == http.StatusNotFound {
		return classify404Error(responseBody)
	}

	// 503 错误：根据响应体智能分类
	if statusCode == http.StatusServiceUnavailable {
		return classify503Error(responseBody)
	}

	// 其他状态码：走表驱动
	meta := getStatusCodeMeta(statusCode)
	return Classification{
		Level:  meta.Level,
		Reason: http.StatusText(statusCode),
	}
}

func isUnexpectedHTMLResponse(contentType string, responseBody []byte) bool {
	if len(responseBody) == 0 {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml") {
		return true
	}
	body := strings.TrimSpace(lowerScanBody(responseBody))
	if !strings.HasPrefix(body, "<!doctype html") && !strings.HasPrefix(body, "<html") {
		return false
	}
	return bodyContainsAny(body, "cloudflare", "cf-chl-", "challenge-platform", "captcha", "enable javascript", "sign in", "log in")
}

func allowsPlainTextMarkers(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return contentType == "" || strings.Contains(contentType, "text/plain") || strings.Contains(contentType, "text/html")
}

func shouldInspectResponseBody(statusCode int, contentType string, responseBody []byte) bool {
	if len(responseBody) == 0 {
		return false
	}
	if statusCode < 200 || statusCode >= 300 {
		return true
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "text/") ||
		strings.Contains(contentType, "event-stream") ||
		strings.Contains(contentType, "xml")
}

type embeddedError struct {
	typeName string
	code     string
	status   string
	message  string
	present  bool
}

func classifyEmbeddedError(statusCode int, responseBody []byte, allowPlainTextMarkers bool) (Classification, bool) {
	if len(responseBody) == 0 {
		return Classification{}, false
	}

	body := lowerScanBody(responseBody)
	detail, sseErrorEvent := parseEmbeddedError(responseBody)
	errorType := strings.ToLower(firstNonEmpty(detail.typeName, detail.code))
	status := strings.ToUpper(detail.status)
	message := strings.ToUpper(detail.message)

	// Anthropic-compatible usage-limit envelopes use either error.type or
	// error.code. They are key/account quota failures even when HTTP status is
	// 200 or when they are delivered as an SSE error event.
	if errorType == "1308" || errorType == "1310" {
		return Classification{Level: ErrorLevelKey, Reason: errorType + " usage quota exceeded"}, true
	}

	// Gemini normally sends this with code=429, but relays may preserve only
	// status/message or wrap it behind HTTP 200. Keep the match structural where
	// possible and retain the bounded text fallback for imperfect gateways.
	if status == "RESOURCE_EXHAUSTED" || strings.Contains(message, "RESOURCE_EXHAUSTED") ||
		(detail.present && strings.Contains(strings.ToUpper(body), "RESOURCE_EXHAUSTED")) {
		return Classification{Level: ErrorLevelKey, Reason: "Gemini RESOURCE_EXHAUSTED quota"}, true
	}

	if isProviderToolCallStateError(body) {
		return Classification{Level: ErrorLevelClient, Reason: "upstream tool call state mismatch"}, true
	}

	switch errorType {
	case "api_error", "overloaded_error", "service_unavailable_error", "server_is_overloaded", "1305":
		return Classification{Level: ErrorLevelChannel, Reason: "upstream " + errorType}, true
	case "rate_limit_error", "rate_limit_exceeded", "too_many_requests", "authentication_error":
		return Classification{Level: ErrorLevelKey, Reason: "upstream " + errorType}, true
	case "invalid_request_error", "validation_error":
		// 二次分诊：invalid_request_error 常被上游代理层拿来包装非用户错误。
		// 生产语料例：
		//   - "Not Found, error: 模型不存在, code: model_not_found, type: invalid_request_error"
		//   - "Bad Request, error: No available channel for model X, type: invalid_request_error"
		//   - "Bad Request, error: 分组 Other 下模型 X 无可用渠道, type: invalid_request_error"
		// 只有真正的用户 payload 错才应停止 failover。
		if bodyContainsAny(body,
			"no available channel",
			"model permission",
			"无可用渠道",
			"distributor",
			"is not supported by any configured account",
			"模型不存在",
			"model_not_supported",
		) {
			return Classification{Level: ErrorLevelChannel, Reason: "upstream " + errorType + " (upstream routing)"}, true
		}
		if bodyContainsAny(body,
			"api key",
			"api_key",
			"invalid_authentication",
			"authentication failed",
			"unauthorized",
		) {
			return Classification{Level: ErrorLevelKey, Reason: "upstream " + errorType + " (auth)"}, true
		}
		// ① 明确的用户 payload 错——跨渠道也无法修复，标记为 deterministic。
		if bodyContainsAny(body,
			"context length",
			"context_length_exceeded",
			"maximum context",
			"prompt is too long",
			"prompt too long",
			"context window",
			"too many tokens",
			"maximum number of tokens",
			"tool schema",
			"invalid tool",
			"invalid function",
			"invalid content",
		) {
			return Classification{Level: ErrorLevelClient, Reason: "deterministic client error"}, true
		}
		return Classification{Level: ErrorLevelClient, Reason: "upstream " + errorType}, true
	}

	// For 2xx only, a structured error envelope or explicit SSE error is still
	// a failure. Unknown SSE error types stay key-scoped (matching the reference
	// classifier's conservative rotation policy); known plain-text soft-error
	// markers receive the same scope as their HTTP counterparts.
	if statusCode >= 200 && statusCode < 300 {
		if detail.present || sseErrorEvent {
			return Classification{Level: ErrorLevelKey, Reason: "HTTP 2xx structured/SSE error"}, true
		}
		trimmedBody := strings.TrimSpace(body)
		jsonLike := strings.HasPrefix(trimmedBody, "{") || strings.HasPrefix(trimmedBody, "[")
		if allowPlainTextMarkers && !jsonLike {
			if bodyContainsAny(body, "rate limit", "rate_limit", "too many requests", "quota exceeded", "insufficient_quota", "请求过快") {
				return Classification{Level: ErrorLevelKey, Reason: "HTTP 2xx rate/quota soft error"}, true
			}
			if bodyContainsAny(body, "overloaded", "service unavailable", "temporarily unavailable", "model overloaded", "负载过高", "服务繁忙") {
				return Classification{Level: ErrorLevelChannel, Reason: "HTTP 2xx upstream soft error"}, true
			}
		}
	}

	return Classification{}, false
}

func isProviderToolCallStateError(body string) bool {
	return strings.Contains(body, "no tool call found for function call output") ||
		(strings.Contains(body, "call_id") && strings.Contains(body, "function call output"))
}
func parseEmbeddedError(responseBody []byte) (embeddedError, bool) {
	scanBody := responseBody
	if len(scanBody) > maxErrorBodyScanBytes {
		scanBody = scanBody[:maxErrorBodyScanBytes]
	}
	if detail, ok := parseEmbeddedErrorJSON(scanBody); ok {
		return detail, false
	}

	sseErrorEvent := false
	for _, rawLine := range strings.Split(string(scanBody), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.EqualFold(line, "event: error") || strings.EqualFold(line, "event:error") {
			sseErrorEvent = true
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if detail, ok := parseEmbeddedErrorJSON([]byte(payload)); ok {
			return detail, sseErrorEvent
		}
	}
	return embeddedError{}, sseErrorEvent
}

func parseEmbeddedErrorJSON(body []byte) (embeddedError, bool) {
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return embeddedError{}, false
	}

	detail := embeddedError{}
	if value, ok := root["error"]; ok && value != nil {
		switch typed := value.(type) {
		case map[string]any:
			detail.present = true
			detail.typeName = mapScalar(typed, "type")
			detail.code = mapScalar(typed, "code")
			detail.status = mapScalar(typed, "status")
			detail.message = mapScalar(typed, "message")
		case string:
			if strings.TrimSpace(typed) != "" {
				detail.present = true
				detail.message = typed
			}
		}
	}
	rootType := mapScalar(root, "type")
	if strings.EqualFold(rootType, "error") || strings.EqualFold(rootType, "response.failed") {
		detail.present = true
	}
	if detail.status == "" {
		detail.status = mapScalar(root, "status")
	}
	return detail, detail.present
}

func mapScalar(value map[string]any, key string) string {
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Classify 基于状态码 + 响应体智能分类错误级别
//
// 分类策略：
//   - 404 做语义分析：明确的 model_not_found → Client 级，其他 → Channel 级
//   - 503 做语义分析：model_not_found/invalid_model → Key 级（权限问题），其他 → Channel 级
//   - 其他状态码：走表驱动分类（statusCodeMetaMap）
func Classify(statusCode int, responseBody []byte) Classification {
	return ClassifyWithHeaders(statusCode, nil, responseBody)
}

func lowerScanBody(responseBody []byte) string {
	if len(responseBody) == 0 {
		return ""
	}
	scanBody := responseBody
	if len(scanBody) > maxErrorBodyScanBytes {
		scanBody = scanBody[:maxErrorBodyScanBytes]
	}
	return strings.ToLower(string(scanBody))
}

func bodyContainsAny(body string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// classify403Error 根据响应体内容智能分类 403 错误
//   - key 权限/额度类 403 保持 Key 级，允许换同渠道其他 key；
//   - 渠道限制当前客户端、IP 或探测请求风控时视为 Channel 级，避免同渠道所有 key 被反复打爆。
func classify403Error(responseBody []byte) Classification {
	bodyLower := lowerScanBody(responseBody)
	if bodyContainsAny(bodyLower,
		"channel:client_restricted",
		"client_restricted",
		"does not allow the current client",
		"current client",
		"ip banned",
		"ip ban",
		"ip restricted",
		"ip blocked",
		"probe request",
		"meaningless content",
		"探测请求",
		"无意义内容",
		"封禁ip",
		"cloudflare",
		"cf-chl-",
		"challenge-platform",
		"captcha",
		"enable javascript",
		"web application firewall",
	) {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "403 channel/client restriction",
		}
	}

	return Classification{
		Level:  ErrorLevelKey,
		Reason: "403 forbidden",
	}
}

// classify404Error 根据响应体内容智能分类 404 错误
//
// 判定顺序：
//  1. 空响应/HTML → channel（URL 或 WAF 配置错）
//  2. 明确的"当前渠道不支持该模型"表述 → channel（换渠道有救）
//  3. model_not_found / does not exist（未提及渠道限定语境）→ client 歧义，
//     兜底交给决策层探测；避免请求真的写错模型时也扫全 group
//  4. 其他 → channel
func classify404Error(responseBody []byte) Classification {
	if len(responseBody) == 0 {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "404 empty response (likely URL misconfiguration)",
		}
	}

	bodyLower := lowerScanBody(responseBody)

	// "当前渠道/分组没配置该模型"—— new-api/one-api 常见语料。
	// 生产实测: "Model \"gpt-5.6-sol-openai-compact\" is not supported by any configured account in this group"
	if bodyContainsAny(bodyLower,
		"is not supported by any configured account",
		"in this group",
		"no available channel",
		"no available account",
		"分组",
		"无可用渠道",
	) {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "404 model unsupported in current channel/group",
		}
	}

	// 通用的 model_not_found / does not exist——可能真的写错模型名，
	// 也可能是某个上游代理没同步模型表。保留 client 语义但让决策层
	// 用有限预算跨渠道探测。
	if strings.Contains(bodyLower, "model_not_found") ||
		strings.Contains(bodyLower, "does not exist") {
		return Classification{
			Level:  ErrorLevelClient,
			Reason: "404 model_not_found",
		}
	}

	// 其他 404 一律视为渠道问题（HTML/JSON/其他）
	return Classification{
		Level:  ErrorLevelChannel,
		Reason: "404 non-model error",
	}
}

// classify503Error 根据响应体内容智能分类 503 错误
// 设计原则：503 默认是渠道级故障，但某些场景实际是 Key 权限问题
//   - model_not_found/invalid_model（Key 级）：当前 key 无权限访问该模型
//   - 其他情况（渠道级）：真正的服务不可用
func classify503Error(responseBody []byte) Classification {
	if len(responseBody) == 0 {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "503 service unavailable",
		}
	}

	bodyLower := lowerScanBody(responseBody)

	if bodyContainsAny(bodyLower,
		"no_available_account",
		"no available account",
		"no available accounts",
		"无可用账号",
		"没有可用账号",
	) {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "503 no available upstream account",
		}
	}

	// Only model/routing-specific unavailability is key-scoped. Generic phrases
	// such as "service temporarily unavailable" describe the endpoint and must
	// skip the remaining keys instead of multiplying the same 503 by key count.
	modelUnavailable := (strings.Contains(bodyLower, "model") && bodyContainsAny(bodyLower,
		"not available",
		"unavailable",
	)) || (strings.Contains(bodyLower, "模型") && bodyContainsAny(bodyLower,
		"无可用",
		"不可用",
	))
	if bodyContainsAny(bodyLower,
		"model_not_found",
		"model not found",
		"invalid_model",
		"model_not_supported",
		"no available channel",
		"无可用渠道",
	) || modelUnavailable {
		return Classification{
			Level:  ErrorLevelKey,
			Reason: "503 model permission error (treat as key-level)",
		}
	}

	// 真正的服务不可用
	return Classification{
		Level:  ErrorLevelChannel,
		Reason: "503 service unavailable",
	}
}

// classify429Error 根据 Retry-After header 和响应体智能分类 429 错误
// 设计原则：
//   - Retry-After > 60s：渠道级限流（全局/IP 限流）
//   - Retry-After <= 60s 或无 header：Key 级限流（账户限流）
func classify429Error(headers http.Header, responseBody []byte) Classification {
	if headers != nil {
		// http.Header.Get() 是大小写无关的
		if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
			// 尝试解析为秒数
			if seconds := parseRetryAfterSeconds(retryAfter); seconds > 60 {
				return Classification{
					Level:  ErrorLevelChannel,
					Reason: "429 rate limit (Retry-After > 60s, likely global/IP limit)",
				}
			}
		}

		// 检查其他限流范围 headers（如 X-RateLimit-Scope）
		if scope := headers.Get("X-RateLimit-Scope"); scope != "" {
			scopeLower := strings.ToLower(scope)
			if scopeLower == "global" || scopeLower == "ip" {
				return Classification{
					Level:  ErrorLevelChannel,
					Reason: "429 rate limit (scope: " + scope + ")",
				}
			}
		}
	}

	bodyLower := lowerScanBody(responseBody)
	if bodyContainsAny(bodyLower,
		"service unavailable",
		"overloaded",
		"temporarily unavailable",
		"upstream unavailable",
		"global rate limit",
		"ip rate limit",
		"全局限流",
		"ip限流",
		"服务不可用",
	) {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "429 upstream/global limit",
		}
	}

	// 默认：Key 级限流
	return Classification{
		Level:  ErrorLevelKey,
		Reason: "429 rate limit (account-level)",
	}
}

// parseRetryAfterSeconds 解析 Retry-After header 为秒数
// 支持两种格式：秒数（"120"）或 HTTP-date（"Wed, 21 Oct 2015 07:28:00 GMT"）
func parseRetryAfterSeconds(retryAfter string) int {
	retryAfter = strings.TrimSpace(retryAfter)
	const maxSeconds = 604800 // 7 天上限（HTTP 规范建议的最大值）

	// 先尝试解析为整数
	seconds := 0
	for _, ch := range retryAfter {
		if ch < '0' || ch > '9' {
			break
		}
		newSeconds := seconds*10 + int(ch-'0')
		// 溢出检测：新值必须 > 旧值 且不超过上限
		if newSeconds > maxSeconds || newSeconds < seconds {
			return maxSeconds
		}
		seconds = newSeconds
	}

	// 如果成功解析到数字，直接返回
	if seconds > 0 {
		return seconds
	}

	// 尝试解析为 HTTP-date（RFC 1123, RFC 850, ANSI C）
	if t, err := http.ParseTime(retryAfter); err == nil {
		duration := int(time.Until(t).Seconds())
		if duration > 0 {
			if duration > maxSeconds {
				return maxSeconds
			}
			return duration
		}
	}

	return 0
}

// classify400Error 根据响应体内容智能分类 400 错误
//
// 上游代理网关（new-api、one-api 及其分叉）经常用 400 传递本应是 5xx/401 的
// 错误。仅按状态码硬编码为 client 会使跨渠道 failover 在遇到"上游代理层
// 问题"时提前退出（生产 14 天 client-terminal 案例的 99% 属于此类）。
//
// 判定顺序（越具体越先）：
//  1. 明确 client 词（真的用户 payload 错，绝不重试）→ client (deterministic)
//  2. key 权限/额度词 → key
//  3. channel 上游代理层词 → channel
//  4. 兜底 → client (ambiguous)，reason "400 bad request"
//
// 兜底的"歧义 client"由 error_decision.applyClientErorRetryPolicy 判定为
// "允许跨渠道探测"（Level 保持 client 不扣健康，Action 改为 retry_channel），
// 探测预算耗尽后再交给 runer 兜底返回。
func classify400Error(responseBody []byte) Classification {
	if len(responseBody) == 0 {
		return Classification{
			Level:  ErrorLevelClient,
			Reason: "400 bad request",
		}
	}

	bodyLower := lowerScanBody(responseBody)

	// ① 明确的用户 payload 错——跨渠道也无法修复，直接返回。
	if bodyContainsAny(bodyLower,
		"context length",
		"context_length_exceeded",
		"maximum context",
		"prompt is too long",
		"prompt too long",
		"context window",
		"too many tokens",
		"maximum number of tokens",
		"tool schema",
		"invalid tool",
		"invalid function",
		"invalid content",
	) {
		return Classification{
			Level:  ErrorLevelClient,
			Reason: "400 deterministic client error",
		}
	}

	// ② key 级：额度/账单 + 上游认证失败（new-api 常见）。
	if bodyContainsAny(bodyLower,
		"quota",
		"insufficient_quota",
		"billing",
		"payment",
		"api key not valid",
		"invalid api key",
		"invalid_api_key",
		"api_key_invalid",
		"authentication failed",
		"authentication_error",
		"invalid_authentication",
		"unauthorized",
	) {
		return Classification{
			Level:  ErrorLevelKey,
			Reason: "400 auth/quota error (treat as key-level)",
		}
	}

	// ③ channel 级：上游代理层路由/配置错，换渠道有救。
	if bodyContainsAny(bodyLower,
		"no available channel",
		"model permission",
		"model_permission",
		"is not supported by any configured account",
		"distributor",
		"无可用渠道",
		"分组",
	) ||
		// new-api 有一个典型 body 解析错的 400：说明上游本身崩了不是 payload 错。
		(strings.Contains(bodyLower, "invalid character") &&
			strings.Contains(bodyLower, "looking for beginning of value")) {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "400 upstream routing error (treat as channel-level)",
		}
	}

	// ④ 兜底：形式上是 400 但语料未命中——保留 client 语义，交给决策层做
	// 有预算的跨渠道探测，reason 保持 "400 bad request" 供 policy 判断歧义。
	return Classification{
		Level:  ErrorLevelClient,
		Reason: "400 bad request",
	}
}

// CanRetryNextKey 判断是否可以在同一渠道尝试下一个 key
// 这是对 Classify 的便捷封装，专门用于 relay 层的重试决策
func CanRetryNextKey(statusCode int, responseBody []byte) bool {
	classification := Classify(statusCode, responseBody)
	return classification.Level == ErrorLevelKey
}
