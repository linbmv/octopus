package relay

import (
	"testing"
)

func TestIsJSONSoftError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "OpenAI error format",
			body: `{"error": {"message": "Rate limit exceeded", "type": "rate_limit_error", "code": "rate_limit_exceeded"}}`,
			want: true,
		},
		{
			name: "simple error object",
			body: `{"error": "something went wrong"}`,
			want: true,
		},
		{
			name: "type is error",
			body: `{"type": "error", "message": "failed"}`,
			want: true,
		},
		{
			name: "valid response",
			body: `{"id": "chatcmpl-123", "object": "chat.completion", "choices": []}`,
			want: false,
		},
		{
			name: "invalid json",
			body: `not json`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJSONSoftError([]byte(tt.body))
			if got != tt.want {
				t.Errorf("isJSONSoftError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsPlainTextSoftError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "rate limit english",
			body: "Error: rate limit exceeded",
			want: true,
		},
		{
			name: "rate limit chinese",
			body: "当前模型负载过高，请稍后再试",
			want: true,
		},
		{
			name: "too many requests",
			body: "Too many requests, please slow down",
			want: true,
		},
		{
			name: "quota exceeded",
			body: "Quota exceeded for this account",
			want: true,
		},
		{
			name: "valid text response",
			body: "Hello! How can I help you today?",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPlainTextSoftError([]byte(tt.body))
			if got != tt.want {
				t.Errorf("isPlainTextSoftError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSSESoftError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "SSE error event",
			body: "event: error\ndata: {\"error\": \"rate limit\"}\n\n",
			want: true,
		},
		{
			name: "rate limit in data",
			body: `data: {"error": {"code": "rate_limit_exceeded", "message": "Rate limit reached"}}

data: [DONE]

`,
			want: true,
		},
		{
			name: "too many requests in data",
			body: `data: {"error": {"code": "too_many_requests"}}

`,
			want: true,
		},
		{
			name: "valid SSE stream",
			body: `data: {"id": "chatcmpl-123", "choices": [{"delta": {"content": "Hello"}}]}

data: [DONE]

`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSSESoftError([]byte(tt.body))
			if got != tt.want {
				t.Errorf("isSSESoftError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSoftError(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		contentType string
		want        bool
	}{
		{
			name:        "200 with JSON error",
			statusCode:  200,
			body:        `{"error": "rate limit"}`,
			contentType: "application/json",
			want:        true,
		},
		{
			name:        "200 with plain text error",
			statusCode:  200,
			body:        "rate limit exceeded",
			contentType: "text/plain",
			want:        true,
		},
		{
			name:        "200 with SSE error",
			statusCode:  200,
			body:        "event: error\ndata: failed\n\n",
			contentType: "text/event-stream",
			want:        true,
		},
		{
			name:        "200 with valid response",
			statusCode:  200,
			body:        `{"id": "123", "object": "chat.completion"}`,
			contentType: "application/json",
			want:        false,
		},
		{
			name:        "non-200 status",
			statusCode:  500,
			body:        `{"error": "internal error"}`,
			contentType: "application/json",
			want:        false,
		},
		{
			name:        "empty body",
			statusCode:  200,
			body:        "",
			contentType: "application/json",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSoftError(tt.statusCode, []byte(tt.body), tt.contentType)
			if got != tt.want {
				t.Errorf("isSoftError() = %v, want %v", got, tt.want)
			}
		})
	}
}
