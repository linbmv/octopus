package relay

import (
	"bytes"
	"encoding/json"
	"strings"
)

// isSoftError 检测响应是否为"软错误"（HTTP 200 但内容是错误）
// 软错误会被视为失败并触发重试
func isSoftError(statusCode int, body []byte, contentType string) bool {
	// 只检测 200 状态码的响应
	if statusCode != 200 {
		return false
	}

	// 空响应不是软错误
	if len(body) == 0 {
		return false
	}

	// JSON 响应软错误检测
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		return isJSONSoftError(body)
	}

	// SSE 流式响应软错误检测
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return isSSESoftError(body)
	}

	// 纯文本错误检测
	return isPlainTextSoftError(body)
}

// isJSONSoftError 检测 JSON 响应中的错误标志
func isJSONSoftError(body []byte) bool {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return false
	}

	// 检测 {"error": {...}} 结构
	if _, hasError := data["error"]; hasError {
		return true
	}

	// 检测 {"type": "error"} 结构
	if typeVal, ok := data["type"].(string); ok && typeVal == "error" {
		return true
	}

	// 检测 OpenAI 错误格式 {"error": {"message": "...", "type": "...", "code": "..."}}
	if errObj, ok := data["error"].(map[string]interface{}); ok {
		if _, hasMessage := errObj["message"]; hasMessage {
			return true
		}
	}

	return false
}

// isSSESoftError 检测 SSE 流中的错误事件
func isSSESoftError(body []byte) bool {
	lines := bytes.Split(body, []byte("\n"))
	for _, line := range lines {
		lineStr := string(line)

		// 检测 SSE error 事件
		if strings.HasPrefix(lineStr, "event: error") {
			return true
		}

		// 检测 data 字段中的限流错误
		if strings.HasPrefix(lineStr, "data:") {
			dataContent := strings.TrimPrefix(lineStr, "data:")
			dataContent = strings.TrimSpace(dataContent)
			if dataContent == "[DONE]" {
				continue
			}

			var eventData map[string]interface{}
			if err := json.Unmarshal([]byte(dataContent), &eventData); err == nil {
				// 检测错误类型字段
				if errType, ok := eventData["error"].(map[string]interface{}); ok {
					if code, hasCode := errType["code"].(string); hasCode {
						if code == "rate_limit_exceeded" || code == "too_many_requests" {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// isPlainTextSoftError 检测纯文本响应中的错误关键词
func isPlainTextSoftError(body []byte) bool {
	bodyLower := strings.ToLower(string(body))

	// 常见错误关键词
	errorKeywords := []string{
		"rate limit",
		"rate_limit",
		"too many requests",
		"负载过高",
		"服务繁忙",
		"quota exceeded",
		"insufficient_quota",
		"model overloaded",
		"模型过载",
		"请求过快",
	}

	for _, keyword := range errorKeywords {
		if strings.Contains(bodyLower, keyword) {
			return true
		}
	}

	return false
}
