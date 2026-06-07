package relay

import (
	"errors"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestIsEndpointUnsupportedErrorByStatusCode(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"404 not found", 404, true},
		{"405 method not allowed", 405, true},
		{"501 not implemented", 501, true},
		{"401 unauthorized", 401, false},
		{"403 forbidden", 403, false},
		{"429 rate limited", 429, false},
		{"500 internal error", 500, false},
		{"502 bad gateway", 502, false},
		{"503 unavailable", 503, false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_httpclient", func(t *testing.T) {
			err := &httpclient.Error{StatusCode: tc.statusCode}
			if got := isEndpointUnsupportedError(err); got != tc.want {
				t.Fatalf("isEndpointUnsupportedError(httpclient status=%d) = %v, 期望 %v", tc.statusCode, got, tc.want)
			}
		})
		t.Run(tc.name+"_llm_response", func(t *testing.T) {
			err := &llm.ResponseError{StatusCode: tc.statusCode}
			if got := isEndpointUnsupportedError(err); got != tc.want {
				t.Fatalf("isEndpointUnsupportedError(ResponseError status=%d) = %v, 期望 %v", tc.statusCode, got, tc.want)
			}
		})
	}
}

func TestIsEndpointUnsupportedErrorByMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"invalid url", "Request failed: Not Found, error: Invalid URL (POST /v1/responses/compact)", true},
		{"no such endpoint", "404 no such endpoint", true},
		{"cannot post", "Cannot POST /v1/responses/compact", true},
		{"unsupported endpoint text", "error: unsupported endpoint", true},
		// 不应误判的情况（通用 "not found" 和 path 回显不等于端点不存在）
		{"auth error with not found text", "401 unauthorized: api key not found", false},
		{"rate limit with path", "429 too many requests on /responses/compact", false},
		{"500 with path echo", "500 internal error calling /responses/compact", false},
		{"generic failure", "connection reset by peer", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEndpointUnsupportedError(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("isEndpointUnsupportedError(%q) = %v, 期望 %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestIsEndpointUnsupportedErrorNil(t *testing.T) {
	if isEndpointUnsupportedError(nil) {
		t.Fatal("nil error 不应判定为端点不支持")
	}
}
