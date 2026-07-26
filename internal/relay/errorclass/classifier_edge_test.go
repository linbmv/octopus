package errorclass

import (
	"net/http"
	"testing"
)

func TestClassify404EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:         "case insensitive model_not_found",
			responseBody: []byte(`{"error": "MODEL_NOT_FOUND"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "case insensitive does not exist",
			responseBody: []byte(`{"error": "The model DOES NOT EXIST"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "partial match model_not_found",
			responseBody: []byte(`{"error": "Upstream error: model_not_found: invalid request"}`),
			wantLevel:    ErrorLevelClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(404, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Errorf("Classify(404, %q) = %v, want %v", tt.responseBody, got.Level, tt.wantLevel)
			}
		})
	}
}

func TestClassify503EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:         "case insensitive model_not_found",
			responseBody: []byte(`{"error": "MODEL_NOT_FOUND"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "case insensitive invalid_model",
			responseBody: []byte(`{"error": "INVALID_MODEL"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "partial match in nested JSON",
			responseBody: []byte(`{"type": "error", "error": {"code": "model_not_found", "message": "Model xyz not available"}}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "Chinese error message",
			responseBody: []byte(`{"error": "分组 Gemini 下模型 deepseek/deepseek-v4-flash 无可用渠道（distributor）"}`),
			wantLevel:    ErrorLevelKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(503, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Errorf("Classify(503, %q) = %v, want %v (reason: %s)", tt.responseBody, got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

func TestClassify429WithHeaders(t *testing.T) {
	tests := []struct {
		name         string
		headers      http.Header
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name: "Retry-After > 60s (channel-level)",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("Retry-After", "120")
				return h
			}(),
			wantLevel: ErrorLevelChannel,
		},
		{
			name: "Retry-After <= 60s (key-level)",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("Retry-After", "30")
				return h
			}(),
			wantLevel: ErrorLevelKey,
		},
		{
			name:      "No Retry-After header (key-level default)",
			headers:   http.Header{},
			wantLevel: ErrorLevelKey,
		},
		{
			name: "X-RateLimit-Scope: global (channel-level)",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("X-RateLimit-Scope", "global")
				return h
			}(),
			wantLevel: ErrorLevelChannel,
		},
		{
			name: "X-RateLimit-Scope: ip (channel-level)",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("X-RateLimit-Scope", "IP")
				return h
			}(),
			wantLevel: ErrorLevelChannel,
		},
		{
			name: "X-RateLimit-Scope: account (key-level)",
			headers: func() http.Header {
				h := http.Header{}
				h.Set("X-RateLimit-Scope", "account")
				return h
			}(),
			wantLevel: ErrorLevelKey,
		},
		{
			name:      "nil headers (key-level default)",
			headers:   nil,
			wantLevel: ErrorLevelKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyWithHeaders(429, tt.headers, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Errorf("ClassifyWithHeaders(429, %v, ...) = %v, want %v (reason: %s)",
					tt.headers, got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

func TestClassify400WithQuotaErrors(t *testing.T) {
	tests := []struct {
		name         string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:         "400 with quota error (key-level)",
			responseBody: []byte(`{"error": {"message": "You exceeded your current quota", "code": "insufficient_quota"}}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "400 with billing error (key-level)",
			responseBody: []byte(`{"error": "Billing issue detected, please update payment method"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "400 with payment error (key-level)",
			responseBody: []byte(`{"error": "Payment required"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "400 generic bad request (client-level)",
			responseBody: []byte(`{"error": "Invalid JSON format"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "400 empty response (client-level)",
			responseBody: []byte{},
			wantLevel:    ErrorLevelClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(400, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Errorf("Classify(400, %q) = %v, want %v (reason: %s)",
					tt.responseBody, got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		want       int
	}{
		{
			name:       "simple seconds",
			retryAfter: "120",
			want:       120,
		},
		{
			name:       "small seconds",
			retryAfter: "30",
			want:       30,
		},
		{
			name:       "zero",
			retryAfter: "0",
			want:       0,
		},
		{
			name:       "HTTP-date (not supported)",
			retryAfter: "Wed, 21 Oct 2015 07:28:00 GMT",
			want:       0,
		},
		{
			name:       "empty string",
			retryAfter: "",
			want:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfterSeconds(tt.retryAfter)
			if got != tt.want {
				t.Errorf("parseRetryAfterSeconds(%q) = %d, want %d", tt.retryAfter, got, tt.want)
			}
		})
	}
}

// TestClassifyWithHeadersCaseInsensitive tests case-insensitive header matching
func TestClassifyWithHeadersCaseInsensitive(t *testing.T) {
	tests := []struct {
		name      string
		headerKey string
		want      ErrorLevel
	}{
		{"standard case", "Retry-After", ErrorLevelChannel},
		{"lowercase", "retry-after", ErrorLevelChannel},
		{"uppercase", "RETRY-AFTER", ErrorLevelChannel},
		{"mixed case", "ReTrY-aFtEr", ErrorLevelChannel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(tt.headerKey, "120")
			got := ClassifyWithHeaders(429, h, nil)
			if got.Level != tt.want {
				t.Errorf("ClassifyWithHeaders with header key %q = %v, want %v", tt.headerKey, got.Level, tt.want)
			}
		})
	}
}

func TestClassify403WAFIsChannelScoped(t *testing.T) {
	got := ClassifyWithHeaders(http.StatusForbidden, http.Header{"Content-Type": {"text/html"}}, []byte(`<!doctype html><html>Cloudflare challenge-platform</html>`))
	if got.Level != ErrorLevelChannel {
		t.Fatalf("ClassifyWithHeaders(403 WAF) = %s (%s), want channel", got.Level.String(), got.Reason)
	}

	ordinary := ClassifyWithHeaders(http.StatusForbidden, http.Header{"Content-Type": {"application/json"}}, []byte(`{"error":"permission denied"}`))
	if ordinary.Level != ErrorLevelKey {
		t.Fatalf("ClassifyWithHeaders(403 permission) = %s (%s), want key", ordinary.Level.String(), ordinary.Reason)
	}
}

func TestClassifyEmbeddedQuotaErrorsAcrossHTTPAndSSE(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       ErrorLevel
		reason     string
	}{
		{
			name:       "Anthropic 1308 behind HTTP 200",
			statusCode: http.StatusOK,
			body:       `{"type":"error","error":{"type":"1308","message":"usage limit reached"}}`,
			want:       ErrorLevelKey,
			reason:     "1308 usage quota exceeded",
		},
		{
			name:       "1308 numeric code behind HTTP 500",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"code":1308,"message":"usage limit reached"}}`,
			want:       ErrorLevelKey,
			reason:     "1308 usage quota exceeded",
		},
		{
			name:       "Gemini RESOURCE_EXHAUSTED",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"Please retry in 59.4s","status":"RESOURCE_EXHAUSTED"}}`,
			want:       ErrorLevelKey,
			reason:     "Gemini RESOURCE_EXHAUSTED quota",
		},
		{
			name:       "Gemini RESOURCE_EXHAUSTED behind HTTP 200 SSE",
			statusCode: http.StatusOK,
			body:       "event: error\ndata: {\"error\":{\"code\":429,\"message\":\"RESOURCE_EXHAUSTED\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n",
			want:       ErrorLevelKey,
			reason:     "Gemini RESOURCE_EXHAUSTED quota",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyWithHeaders(test.statusCode, nil, []byte(test.body))
			if got.Level != test.want || got.Reason != test.reason {
				t.Fatalf("classification = %#v, want level=%s reason=%q", got, test.want, test.reason)
			}
		})
	}
}

func TestClassifyHTTP200SoftAndSSEErrorsDynamically(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ErrorLevel
	}{
		{name: "SSE overload", body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n", want: ErrorLevelChannel},
		{name: "SSE rate limit", body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}\n\n", want: ErrorLevelKey},
		{name: "SSE invalid request", body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\"}}\n\n", want: ErrorLevelClient},
		{name: "SSE unknown error", body: "event: error\ndata: failed\n\n", want: ErrorLevelKey},
		{name: "JSON rate limit", body: `{"error":{"code":"rate_limit_exceeded"}}`, want: ErrorLevelKey},
		{name: "plain rate limit", body: "rate limit exceeded", want: ErrorLevelKey},
		{name: "plain overload", body: "service unavailable: model overloaded", want: ErrorLevelChannel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyWithHeaders(http.StatusOK, nil, []byte(test.body))
			if got.Level != test.want {
				t.Fatalf("classification = %#v, want %s", got, test.want)
			}
		})
	}
}

func TestClassifyResponseUsesContentTypeAtSingleEntryPoint(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        ErrorLevel
	}{
		{name: "JSON error", contentType: "application/json", body: `{"error":{"type":"rate_limit_error"}}`, want: ErrorLevelKey},
		{name: "SSE error", contentType: "text/event-stream", body: "event: error\ndata: failed\n\n", want: ErrorLevelKey},
		{name: "plain overload", contentType: "text/plain", body: "service unavailable", want: ErrorLevelChannel},
		{name: "WAF HTML behind HTTP 200", contentType: "text/html; charset=utf-8", body: `<!doctype html><html><title>Just a moment...</title></html>`, want: ErrorLevelChannel},
		{name: "JSON success with null error", contentType: "application/json", body: `{"id":"ok","error":null}`, want: ErrorLevelNone},
		{name: "binary payload is not scanned", contentType: "application/octet-stream", body: "rate limit", want: ErrorLevelNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyResponse(http.StatusOK, nil, []byte(test.body), test.contentType)
			if got.Level != test.want {
				t.Fatalf("ClassifyResponse() = %#v, want level %s", got, test.want)
			}
		})
	}
}

func TestClassifyHTTP200SuccessfulPayloadDoesNotMatchEmbeddedUserText(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"message":{"content":"The phrase rate limit is documentation."}}]}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n",
	} {
		got := ClassifyWithHeaders(http.StatusOK, nil, []byte(body))
		if got.Level != ErrorLevelNone {
			t.Fatalf("successful payload classified as %#v: %s", got, body)
		}
	}
}

// TestLargeResponseBodyPerformance tests that large response bodies are handled efficiently
func TestLargeResponseBodyPerformance(t *testing.T) {
	// 创建一个 10MB 的响应体（模拟 HTML 错误页）
	largeBody := make([]byte, 10*1024*1024)
	for i := range largeBody {
		largeBody[i] = 'x'
	}
	// 在前 8KB 之外添加关键字（不应该被匹配到）
	copy(largeBody[9*1024*1024:], []byte("model_not_found"))

	// 503 错误应该被分类为 Channel 级（因为 model_not_found 在扫描窗口之外）
	got := Classify(503, largeBody)
	if got.Level != ErrorLevelChannel {
		t.Errorf("Classify(503, large body with late model_not_found) = %v, want %v", got.Level, ErrorLevelChannel)
	}

	// 在前 8KB 内添加关键字
	copy(largeBody[100:], []byte("model_not_found"))
	got = Classify(503, largeBody)
	if got.Level != ErrorLevelKey {
		t.Errorf("Classify(503, large body with early model_not_found) = %v, want %v", got.Level, ErrorLevelKey)
	}
}

// TestClassify400WithProxyLayerErrors 覆盖 P0：new-api / 上游代理层用 400 传
// 递的可跨渠道恢复错误必须离开 client 级，避免 runner 中断 failover。
func TestClassify400WithProxyLayerErrors(t *testing.T) {
	tests := []struct {
		name         string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:         "api key not valid (Gemini via new-api)",
			responseBody: []byte(`{"error":{"message":"API key not valid. Please pass a valid API key.","code":400}}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "invalid api key (OpenAI-compatible)",
			responseBody: []byte(`{"error":{"type":"invalid_request_error","message":"Invalid API key provided"}}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "unauthorized wording behind 400",
			responseBody: []byte(`{"error":"unauthorized: bearer token rejected"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "no available channel (new-api routing)",
			responseBody: []byte(`{"error":{"message":"No available channel for model gemini-3.5-flash under group Other (distributor)","code":"model_not_found","type":"new_api_error"}}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "chinese no available channel",
			responseBody: []byte(`{"error":"分组 Other 下模型 gemini-3.5-flash 无可用渠道（distributor）"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "new-api body parse failure",
			responseBody: []byte(`{"error":"Invalid request: Invalid request: invalid character 'd' looking for beginning of value","type":"new_api_error"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "model not supported by group",
			responseBody: []byte(`{"error":"Model gpt-5.6-sol is not supported by any configured account in this group","type":"new_api_error"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "context length is real client error",
			responseBody: []byte(`{"error":{"message":"This model's maximum context length is 128000 tokens, however you requested 200000","type":"invalid_request_error","code":"context_length_exceeded"}}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "prompt too long is real client error",
			responseBody: []byte(`{"error":"Prompt is too long: 5000 tokens > 4096 limit"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "invalid tool schema is real client error",
			responseBody: []byte(`{"error":"invalid tool schema at tools[2].function.parameters"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "generic 400 is ambiguous client",
			responseBody: []byte(`{"error":"Bad Request"}`),
			wantLevel:    ErrorLevelClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(400, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Fatalf("level = %v, want %v (reason=%q)", got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

// TestClassify404WithGroupScopedModelNotFound 覆盖 P0：当 model_not_found 明确
// 来自"当前 group 无可用后端"时应属 channel 级；无 group 语境时保持 client。
func TestClassify404WithGroupScopedModelNotFound(t *testing.T) {
	tests := []struct {
		name         string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:         "model not supported by group is channel",
			responseBody: []byte(`{"error":"Model X is not supported by any configured account in this group","code":"model_not_found"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "model not available in this group is channel",
			responseBody: []byte(`{"error":"分组 default 下模型 X 无可用渠道"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "raw model_not_found without group is client",
			responseBody: []byte(`{"error":"model_not_found: unknown-model"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "raw does not exist without group is client",
			responseBody: []byte(`{"error":"The model does not exist"}`),
			wantLevel:    ErrorLevelClient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(404, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Fatalf("level = %v, want %v (reason=%q)", got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

// TestClassifyEmbeddedInvalidRequestError 覆盖 P0：invalid_request_error 需要
// 按 body 二次分诊，只有真的 client 语义才保持 client。
func TestClassifyEmbeddedInvalidRequestError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantLevel  ErrorLevel
	}{
		{
			name:       "invalid_request with no available channel is channel",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"No available channel for model X"}}`,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "invalid_request with 404 model_not_found in group is channel",
			statusCode: http.StatusNotFound,
			body:       `{"error":{"type":"invalid_request_error","message":"Model X is not supported by any configured account in this group","code":"model_not_found"}}`,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "invalid_request with api key wording is key",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"Invalid api key"}}`,
			wantLevel:  ErrorLevelKey,
		},
		{
			name:       "invalid_request with context_length_exceeded stays client",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Prompt is too long"}}`,
			wantLevel:  ErrorLevelClient,
		},
		{
			name:       "invalid_request with tool call state stays client",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"No tool call found for function call output with call_id call_abc"}}`,
			wantLevel:  ErrorLevelClient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyWithHeaders(tt.statusCode, nil, []byte(tt.body))
			if got.Level != tt.wantLevel {
				t.Fatalf("level = %v, want %v (reason=%q)", got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}


// TestHTTPDateParsing tests HTTP-date format in Retry-After header
func TestHTTPDateParsing(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		wantLevel  ErrorLevel
	}{
		{
			name:       "future HTTP-date (should be channel-level if > 60s)",
			retryAfter: "Wed, 21 Oct 2099 07:28:00 GMT",
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "past HTTP-date (should be key-level, treated as 0)",
			retryAfter: "Wed, 21 Oct 2000 07:28:00 GMT",
			wantLevel:  ErrorLevelKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Retry-After", tt.retryAfter)
			got := ClassifyWithHeaders(429, h, nil)
			if got.Level != tt.wantLevel {
				t.Errorf("ClassifyWithHeaders(429, Retry-After=%q) = %v, want %v", tt.retryAfter, got.Level, tt.wantLevel)
			}
		})
	}
}
