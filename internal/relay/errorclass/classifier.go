package errorclass

import (
	"net/http"
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
// 设计原则：404 本身是异常情况，只有明确的客户端错误才不切换
//   - 模型不存在（客户端级）：明确的 model_not_found 或 does not exist
//   - 其他情况（渠道级）：空响应、HTML、异常 JSON 等都应切换渠道
func classify404Error(responseBody []byte) Classification {
	if len(responseBody) == 0 {
		return Classification{
			Level:  ErrorLevelChannel,
			Reason: "404 empty response (likely URL misconfiguration)",
		}
	}

	bodyLower := lowerScanBody(responseBody)

	// 仅当明确是"模型不存在"时才视为客户端错误
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

	// 识别上游返回的 model_not_found / invalid_model / model_not_supported 等权限相关错误
	// 同时支持英文通用表述和中文错误信息
	if strings.Contains(bodyLower, "model_not_found") ||
		strings.Contains(bodyLower, "model not found") ||
		strings.Contains(bodyLower, "invalid_model") ||
		strings.Contains(bodyLower, "model_not_supported") ||
		strings.Contains(bodyLower, "not available") ||
		strings.Contains(bodyLower, "no available") ||
		strings.Contains(bodyLower, "unavailable") ||
		strings.Contains(bodyLower, "无可用") ||
		strings.Contains(bodyLower, "不可用") {
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
// 设计原则：400 默认是客户端错误，但某些上游会用 400 返回权限/配额问题
func classify400Error(responseBody []byte) Classification {
	if len(responseBody) == 0 {
		return Classification{
			Level:  ErrorLevelClient,
			Reason: "400 bad request",
		}
	}

	bodyLower := lowerScanBody(responseBody)

	// 识别伪装成 400 的权限/配额错误
	if strings.Contains(bodyLower, "quota") ||
		strings.Contains(bodyLower, "insufficient_quota") ||
		strings.Contains(bodyLower, "billing") ||
		strings.Contains(bodyLower, "payment") {
		return Classification{
			Level:  ErrorLevelKey,
			Reason: "400 quota/billing error (treat as key-level)",
		}
	}

	// 真正的客户端错误
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
