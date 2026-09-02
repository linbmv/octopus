package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/looplj/axonhub/llm/httpclient"
)

func jsonRequest(body string) *httpclient.Request {
	return &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(body),
	}
}

func TestApplyChannelConfigHeaderRuleActions(t *testing.T) {
	request := jsonRequest(`{}`)
	request.Headers.Set("X-Keep", "one")
	request.Headers.Set("X-Drop", "gone")

	channel := &model.Channel{
		CustomHeader: []model.CustomHeader{{HeaderKey: "X-Base", HeaderValue: "base"}},
		HeaderRules: []model.HeaderRule{
			{Action: "set", HeaderKey: "X-Base", HeaderValue: "overridden"},
			{Action: "append", HeaderKey: "X-Keep", HeaderValue: "two"},
			{Action: "remove", HeaderKey: "X-Drop"},
		},
	}
	if err := applyChannelConfig(*channel, request); err != nil {
		t.Fatalf("applyChannelConfig: %v", err)
	}

	if got := request.Headers.Get("X-Base"); got != "overridden" {
		t.Errorf("set rule should run after CustomHeader, got %q", got)
	}
	if got := request.Headers.Values("X-Keep"); len(got) != 2 || got[1] != "two" {
		t.Errorf("append rule should preserve existing value, got %v", got)
	}
	if _, ok := request.Headers["X-Drop"]; ok {
		t.Error("remove rule should delete the header")
	}
}

func TestApplyChannelConfigRejectsCredentialHeaderRewrite(t *testing.T) {
	request := jsonRequest(`{}`)
	request.Headers.Set("Authorization", "Bearer real-upstream-key")

	channel := &model.Channel{
		CustomHeader: []model.CustomHeader{{HeaderKey: "Authorization", HeaderValue: "Bearer attacker"}},
		HeaderRules: []model.HeaderRule{
			{Action: "remove", HeaderKey: "Authorization"},
			{Action: "set", HeaderKey: "chatgpt-account-id", HeaderValue: "attacker"},
		},
	}
	if err := applyChannelConfig(*channel, request); err != nil {
		t.Fatalf("applyChannelConfig: %v", err)
	}

	if got := request.Headers.Get("Authorization"); got != "Bearer real-upstream-key" {
		t.Errorf("credential header must survive rewrite, got %q", got)
	}
	if request.Headers.Get("chatgpt-account-id") != "" {
		t.Error("account identity header must not be settable by channel config")
	}
}

func TestApplyChannelConfigJSONRewriteRules(t *testing.T) {
	value := `"custom"`
	channel := &model.Channel{
		JSONRewriteRules: []model.JSONRewriteRule{
			{Action: "override", Path: "/tools/0/type", Value: &value},
			{Action: "remove", Path: "/stream"},
			{Action: "remove", Path: "/missing/path"},
		},
	}
	request := jsonRequest(`{"stream":true,"tools":[{"type":"function"}]}`)
	if err := applyChannelConfig(*channel, request); err != nil {
		t.Fatalf("applyChannelConfig: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(request.Body, &got); err != nil {
		t.Fatalf("rewritten body is not valid JSON: %v", err)
	}
	if _, ok := got["stream"]; ok {
		t.Error("remove rule should delete the key")
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools array should be preserved, got %#v", got["tools"])
	}
	if entry := tools[0].(map[string]any); entry["type"] != "custom" {
		t.Errorf("override rule should set nested value, got %#v", entry["type"])
	}
}

func TestApplyChannelConfigSkipsRewriteForNonJSONBody(t *testing.T) {
	value := `"custom"`
	channel := &model.Channel{
		JSONRewriteRules: []model.JSONRewriteRule{{Action: "override", Path: "/model", Value: &value}},
	}
	request := &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"multipart/form-data; boundary=xyz"}},
		Body:    []byte("--xyz\r\nnot json\r\n--xyz--"),
	}
	if err := applyChannelConfig(*channel, request); err != nil {
		t.Fatalf("multipart body should be left alone, got error: %v", err)
	}
	if string(request.Body) != "--xyz\r\nnot json\r\n--xyz--" {
		t.Error("multipart body must not be rewritten as JSON")
	}
}

func TestApplyChannelConfigReportsInvalidRewriteRule(t *testing.T) {
	channel := &model.Channel{
		JSONRewriteRules: []model.JSONRewriteRule{{Action: "override", Path: "/model"}},
	}
	request := jsonRequest(`{"model":"a"}`)
	if err := applyChannelConfig(*channel, request); err == nil {
		t.Fatal("override without a value should fail loudly")
	}
	if string(request.Body) != `{"model":"a"}` {
		t.Error("failed rewrite must leave the original body untouched")
	}
}

func TestSelectChannelEndpointRotatesAvailableCredentialsAndURLs(t *testing.T) {
	channel := model.Channel{
		Name:    "multi",
		BaseURL: "https://legacy.example",
		BaseUrls: []model.BaseUrl{
			{URL: "https://slow.example", Delay: 20},
			{URL: "https://fast.example", Delay: 0},
		},
		Key: "legacy-key",
		Keys: []model.ChannelKey{
			{ID: 2, Enabled: true, ChannelKey: "key-b"},
			{ID: 1, Enabled: true, ChannelKey: "key-a"},
			{ID: 3, Enabled: false, ChannelKey: "disabled"},
		},
	}
	first, firstKeyID, err := selectChannelEndpoint(channel, 0)
	if err != nil {
		t.Fatalf("first endpoint: %v", err)
	}
	if first.BaseURL != "https://fast.example" || first.Key != "key-a" {
		t.Fatalf("first endpoint = %#v", first)
	}
	if firstKeyID != 1 {
		t.Fatalf("first key id = %d, want 1", firstKeyID)
	}
	second, secondKeyID, err := selectChannelEndpoint(channel, 1)
	if err != nil {
		t.Fatalf("second endpoint: %v", err)
	}
	if second.BaseURL != "https://slow.example" || second.Key != "key-b" {
		t.Fatalf("second endpoint = %#v", second)
	}
	if secondKeyID != 2 {
		t.Fatalf("second key id = %d, want 2", secondKeyID)
	}
}

func TestSelectChannelEndpointRejectsAllDisabledCredentials(t *testing.T) {
	_, _, err := selectChannelEndpoint(model.Channel{
		Name: "disabled",
		Keys: []model.ChannelKey{{ID: 1, Enabled: false, ChannelKey: "disabled"}},
	}, 0)
	if err == nil {
		t.Fatal("expected no available credential error")
	}
}

// 无独立凭据的渠道沿用 Channel.Key, 此时不应报告凭据 ID, 否则会写错健康态。
func TestSelectChannelEndpointLegacyKeyReportsNoKeyID(t *testing.T) {
	selected, keyID, err := selectChannelEndpoint(model.Channel{
		Name:    "legacy",
		BaseURL: "https://legacy.example",
		Key:     "legacy-key",
	}, 3)
	if err != nil {
		t.Fatalf("legacy endpoint: %v", err)
	}
	if selected.Key != "legacy-key" || selected.BaseURL != "https://legacy.example" {
		t.Fatalf("legacy endpoint = %#v", selected)
	}
	if keyID != 0 {
		t.Fatalf("legacy key id = %d, want 0", keyID)
	}
}

// 冷却中的凭据必须被跳过, 未冷却的凭据仍要被选中。
func TestSelectChannelEndpointSkipsCooledCredential(t *testing.T) {
	future := time.Now().Unix() + 600
	selected, keyID, err := selectChannelEndpoint(model.Channel{
		Name: "cooled",
		Keys: []model.ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "cooling", StatusCode: 429, RetryAfterUntil: future},
			{ID: 2, Enabled: true, ChannelKey: "healthy"},
		},
	}, 0)
	if err != nil {
		t.Fatalf("cooled endpoint: %v", err)
	}
	if selected.Key != "healthy" || keyID != 2 {
		t.Fatalf("selected = %q key id = %d, want healthy/2", selected.Key, keyID)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name  string
		value string
		want  int64
	}{
		{name: "absent", value: "", want: 0},
		{name: "seconds", value: "42", want: 42},
		{name: "negative", value: "-5", want: 0},
		{name: "http date", value: now.Add(90 * time.Second).UTC().Format(http.TimeFormat), want: 90},
		{name: "past date", value: now.Add(-90 * time.Second).UTC().Format(http.TimeFormat), want: 0},
		{name: "garbage", value: "soon", want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			header := http.Header{}
			if testCase.value != "" {
				header.Set("Retry-After", testCase.value)
			}
			if got := parseRetryAfterSeconds(header, now); got != testCase.want {
				t.Fatalf("parseRetryAfterSeconds(%q) = %d, want %d", testCase.value, got, testCase.want)
			}
		})
	}
	if got := parseRetryAfterSeconds(nil, now); got != 0 {
		t.Fatalf("nil header = %d, want 0", got)
	}
}

func TestUpstreamFailureStatusExtractsStatusAndRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	failure := &httpclient.Error{
		StatusCode: http.StatusTooManyRequests,
		Headers:    http.Header{"Retry-After": []string{"30"}},
	}
	status, retryAfter := upstreamFailureStatus(fmt.Errorf("wrapped: %w", failure), now)
	if status != http.StatusTooManyRequests || retryAfter != 30 {
		t.Fatalf("status = %d retryAfter = %d, want 429/30", status, retryAfter)
	}
	// 网络层失败没有 HTTP 状态码, 不应被当作限流。
	if status, retryAfter := upstreamFailureStatus(errors.New("dial tcp: timeout"), now); status != 0 || retryAfter != 0 {
		t.Fatalf("network failure = %d/%d, want 0/0", status, retryAfter)
	}
}

// 传输层失败没有响应可分类, 必须按渠道级处理以便继续 failover, 而不是终止请求。
func TestClassifyUpstreamFailureTreatsTransportErrorAsChannelLevel(t *testing.T) {
	class := classifyUpstreamFailure(errors.New("dial tcp 10.0.0.1:443: i/o timeout"))
	if class.Level != errorclass.ErrorLevelChannel {
		t.Fatalf("transport failure level = %v, want channel", class.Level)
	}
}

// 客户端自身请求不合法时继续换渠道只会放大失败, 必须判为请求级并终止。
func TestClassifyUpstreamFailureDetectsClientLevelBadRequest(t *testing.T) {
	failure := &httpclient.Error{
		StatusCode: http.StatusBadRequest,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"error":{"message":"max_tokens: must be <= 8192","type":"invalid_request_error"}}`),
	}
	class := classifyUpstreamFailure(fmt.Errorf("upstream: %w", failure))
	if class.Level != errorclass.ErrorLevelClient {
		t.Fatalf("invalid request level = %v, want client", class.Level)
	}
}

// 401/403 这类凭据失效应停在 key 级, 以便同渠道换凭据重试而不是整渠道熔断。
func TestClassifyUpstreamFailureDetectsKeyLevelAuthFailure(t *testing.T) {
	failure := &httpclient.Error{
		StatusCode: http.StatusUnauthorized,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"error":{"message":"invalid api key"}}`),
	}
	if class := classifyUpstreamFailure(failure); class.Level != errorclass.ErrorLevelKey {
		t.Fatalf("auth failure level = %v, want key", class.Level)
	}
}

// 上游 5xx 属于渠道故障, 应保持渠道级以触发 failover 与冷却。
func TestClassifyUpstreamFailureTreatsServerErrorAsChannelLevel(t *testing.T) {
	failure := &httpclient.Error{
		StatusCode: http.StatusBadGateway,
		Body:       []byte("bad gateway"),
	}
	if class := classifyUpstreamFailure(failure); class.Level != errorclass.ErrorLevelChannel {
		t.Fatalf("server error level = %v, want channel", class.Level)
	}
}
