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
			name:         "403 client restricted (channel error)",
			statusCode:   403,
			responseBody: []byte(`{"error":"This channel does not allow the current client","code":"channel:client_restricted"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:         "403 probe/access denied (channel error)",
			statusCode:   403,
			responseBody: []byte(`{"error":"请勿发送探测请求和无意义内容，多次发送探测请求将封禁IP。","code":"access_denied"}`),
			wantLevel:    ErrorLevelChannel,
		},
		{
			name:       "429 Too Many Requests",
			statusCode: 429,
			wantLevel:  ErrorLevelKey,
		},
		{
			name:         "429 service unavailable body (channel error)",
			statusCode:   429,
			responseBody: []byte(`{"error":"Service Unavailable","type":"api_error"}`),
			wantLevel:    ErrorLevelChannel,
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
			name:         "503 + no_available_account (channel error)",
			statusCode:   503,
			responseBody: []byte(`{"error":"无可用账号，请稍后重试","code":"no_available_account"}`),
			wantLevel:    ErrorLevelChannel,
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
			name:         "403 client restricted should NOT retry next key",
			statusCode:   403,
			responseBody: []byte(`{"code":"channel:client_restricted","error":"This channel does not allow the current client"}`),
			want:         false,
		},
		{
			name:         "429 service unavailable should NOT retry next key",
			statusCode:   429,
			responseBody: []byte(`{"error":"Service Unavailable"}`),
			want:         false,
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
			name:         "503 no_available_account should NOT retry next key",
			statusCode:   503,
			responseBody: []byte(`{"code":"no_available_account","error":"无可用账号，请稍后重试"}`),
			want:         false,
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
