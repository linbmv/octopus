package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
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

func TestCompactEndpointDowngradeTriesResponsesBeforeChat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			_, _ = w.Write([]byte(`{
				"object": "response",
				"id": "resp_1",
				"created_at": 1,
				"model": "gpt-5.5",
				"status": "completed",
				"output": [
					{
						"type": "message",
						"role": "assistant",
						"content": [{"type": "output_text", "text": "ok"}]
					}
				]
			}`))
		case "/v1/chat/completions":
			t.Fatalf("不应在普通 /responses 成功后继续请求 Chat 端点")
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "responses-only",
		Type:     llm.APIFormatOpenAIResponse,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}},
	}
	internalRequest := &llm.Request{
		Model:       "gpt-5.5",
		APIFormat:   llm.APIFormatOpenAIResponseCompact,
		RequestType: llm.RequestTypeCompact,
		RawRequest: &httpclient.Request{
			Method:  http.MethodPost,
			Path:    "/v1/responses/compact",
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"hello"}]}`),
		},
		Compact: &llm.CompactRequest{
			Input: []llm.Message{compactInputMessage("user", "hello")},
		},
	}

	outAdapter, err := newOutbound(channel.Type, internalRequest, channel.GetBaseUrl(), "test-key")
	if err != nil {
		t.Fatalf("newOutbound returned error: %v", err)
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	ra := &relayAttempt{
		relayRun: &relayRun{
			c:               ginCtx,
			inAdapter:       newInbound(llm.APIFormatOpenAIResponseCompact),
			internalRequest: internalRequest,
			metrics:         &RelayMetrics{ActualModel: internalRequest.Model},
		},
		outAdapter: outAdapter,
		channel:    channel,
		usedKey:    dbmodel.ChannelKey{ID: 1, ChannelKey: "test-key"},
	}

	statusCode, err := ra.forward()
	if err != nil {
		t.Fatalf("forward returned error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, 期望 200", statusCode)
	}

	want := []string{"/v1/responses/compact", "/v1/responses"}
	if len(paths) != len(want) {
		t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
		}
	}
}
