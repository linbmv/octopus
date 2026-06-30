package errorclass

import (
	"testing"
)

func TestClassifyStatusCodes(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		// === 2xx 成功 ===
		{
			name:       "200 OK",
			statusCode: 200,
			wantLevel:  ErrorLevelNone,
		},
		{
			name:       "201 Created",
			statusCode: 201,
			wantLevel:  ErrorLevelNone,
		},

		// === Key 级错误 ===
		{
			name:       "401 Unauthorized",
			statusCode: 401,
			wantLevel:  ErrorLevelKey,
		},
		{
			name:       "403 Forbidden",
			statusCode: 403,
			wantLevel:  ErrorLevelKey,
		},
		{
			name:       "429 Too Many Requests",
			statusCode: 429,
			wantLevel:  ErrorLevelKey,
		},

		// === 渠道级错误 ===
		{
			name:       "500 Internal Server Error",
			statusCode: 500,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "502 Bad Gateway",
			statusCode: 502,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "503 Service Unavailable (generic)",
			statusCode: 503,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "504 Gateway Timeout",
			statusCode: 504,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "520 Cloudflare Unknown Error",
			statusCode: 520,
			wantLevel:  ErrorLevelChannel,
		},

		// === 客户端错误 ===
		{
			name:       "400 Bad Request",
			statusCode: 400,
			wantLevel:  ErrorLevelClient,
		},
		{
			name:       "406 Not Acceptable",
			statusCode: 406,
			wantLevel:  ErrorLevelClient,
		},
		{
			name:       "413 Payload Too Large",
			statusCode: 413,
			wantLevel:  ErrorLevelClient,
		},

		// === 404 智能分类 ===
		{
			name:         "404 model_not_found (client error)",
			statusCode:   404,
			responseBody: []byte(`{"error": {"message": "model 'gpt-5' not found", "code": "model_not_found"}}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "404 model does not exist (client error)",
			statusCode:   404,
			responseBody: []byte(`{"error": "The model does not exist"}`),
			wantLevel:    ErrorLevelClient,
		},
		{
			name:         "404 empty response (channel error)",
			statusCode:   404,
			responseBody: []byte{},
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "404 HTML error page (channel error)",
			statusCode:   404,
			responseBody: []byte(`<html><body>Not Found</body></html>`),
			wantLevel:    ErrorLevelChannel,
		},

		// === 503 智能分类 ===
		{
			name:         "503 + model_not_found (key error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "分组 Gemini 下模型 deepseek/deepseek-v4-flash 无可用渠道（distributor）", "code": "model_not_found"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "503 + model not found (key error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "Request failed: Service Unavailable, error: model not found"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "503 + invalid_model (key error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "invalid_model: the model is not supported"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "503 + model_not_supported (key error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "model_not_supported"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "503 + 无可用渠道 (key error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "分组下无可用渠道"}`),
			wantLevel:    ErrorLevelKey,
		},
		{
			name:         "503 generic timeout (channel error)",
			statusCode:   503,
			responseBody: []byte(`{"error": "upstream timeout"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "503 empty response (channel error)",
			statusCode:   503,
			responseBody: []byte{},
			wantLevel:    ErrorLevelChannel,
		},

		// === 未知状态码兜底 ===
		{
			name:       "599 unknown 5xx (channel error)",
			statusCode: 599,
			wantLevel:  ErrorLevelChannel,
		},
		{
			name:       "499 client closed request (channel error)",
			statusCode: 499,
			wantLevel:  ErrorLevelChannel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.statusCode, tt.responseBody)
			if got.Level != tt.wantLevel {
				t.Errorf("Classify(%d, %q) = %v, want %v (reason: %s)",
					tt.statusCode, tt.responseBody, got.Level, tt.wantLevel, got.Reason)
			}
		})
	}
}

func TestCanRetryNextKey(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody []byte
		want         bool
	}{
		{
			name:       "401 should retry next key",
			statusCode: 401,
			want:       true,
		},
		{
			name:       "403 should retry next key",
			statusCode: 403,
			want:       true,
		},
		{
			name:       "429 should retry next key",
			statusCode: 429,
			want:       true,
		},
		{
			name:         "503 + model_not_found should retry next key",
			statusCode:   503,
			responseBody: []byte(`{"error": "model_not_found"}`),
			want:         true,
		},
		{
			name:         "503 + 无可用渠道 should retry next key",
			statusCode:   503,
			responseBody: []byte(`{"error": "分组 Gemini 下模型无可用渠道"}`),
			want:         true,
		},
		{
			name:       "500 should NOT retry next key",
			statusCode: 500,
			want:       false,
		},
		{
			name:       "502 should NOT retry next key",
			statusCode: 502,
			want:       false,
		},
		{
			name:         "503 generic should NOT retry next key",
			statusCode:   503,
			responseBody: []byte(`{"error": "upstream timeout"}`),
			want:         false,
		},
		{
			name:       "400 should NOT retry next key",
			statusCode: 400,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanRetryNextKey(tt.statusCode, tt.responseBody)
			if got != tt.want {
				t.Errorf("CanRetryNextKey(%d, %q) = %v, want %v",
					tt.statusCode, tt.responseBody, got, tt.want)
			}
		})
	}
}

func TestErrorLevelString(t *testing.T) {
	tests := []struct {
		level ErrorLevel
		want  string
	}{
		{ErrorLevelNone, "none"},
		{ErrorLevelKey, "key"},
		{ErrorLevelChannel, "channel"},
		{ErrorLevelClient, "client"},
		{ErrorLevel(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.level.String()
			if got != tt.want {
				t.Errorf("ErrorLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

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
		headers      map[string]string
		responseBody []byte
		wantLevel    ErrorLevel
	}{
		{
			name:      "Retry-After > 60s (channel-level)",
			headers:   map[string]string{"Retry-After": "120"},
			wantLevel: ErrorLevelChannel,
		},
		{
			name:      "Retry-After <= 60s (key-level)",
			headers:   map[string]string{"Retry-After": "30"},
			wantLevel: ErrorLevelKey,
		},
		{
			name:      "No Retry-After header (key-level default)",
			headers:   map[string]string{},
			wantLevel: ErrorLevelKey,
		},
		{
			name:      "X-RateLimit-Scope: global (channel-level)",
			headers:   map[string]string{"X-RateLimit-Scope": "global"},
			wantLevel: ErrorLevelChannel,
		},
		{
			name:      "X-RateLimit-Scope: ip (channel-level)",
			headers:   map[string]string{"X-RateLimit-Scope": "IP"},
			wantLevel: ErrorLevelChannel,
		},
		{
			name:      "X-RateLimit-Scope: account (key-level)",
			headers:   map[string]string{"X-RateLimit-Scope": "account"},
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

