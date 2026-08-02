package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

func TestBuildFinalOutboundRequestUsesDedicatedCodexOAuthAdapter(t *testing.T) {
	const (
		accessToken = "header.payload.signature"
		accountID   = "account-from-document"
	)
	raw := &httpclient.Request{
		Method: http.MethodPost, Path: "/v1/chat/completions", ContentType: "application/json",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"model":"gpt-5.3-codex","stream":false,
			"messages":[{"role":"user","content":[
				{"type":"text","text":"inspect"},
				{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}
			]}]
		}`),
	}
	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	internalRequest, err := inbound.TransformRequest(context.Background(), raw)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	internalRequest.RawRequest = raw
	credential := fmt.Sprintf(`{"type":"codex","access_token":%q,"refresh_token":"refresh","account_id":%q,"expired":"2099-01-01T00:00:00Z"}`, accessToken, accountID)
	channel := &dbmodel.Channel{Type: dbmodel.ChannelTypeOpenAICodex}
	result, err := BuildFinalOutboundRequest(context.Background(), channel, dbmodel.ChannelKey{ChannelKey: credential}, "https://chatgpt.com/backend-api/codex", internalRequest)
	if err != nil {
		t.Fatalf("BuildFinalOutboundRequest error: %v", err)
	}
	request := result.Request
	if request.URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("URL = %q", request.URL)
	}
	if request.Auth == nil || request.Auth.Type != httpclient.AuthTypeBearer || request.Auth.APIKey != accessToken {
		t.Fatal("Codex request did not use the OAuth access token as bearer authentication")
	}
	if request.Headers.Get("Chatgpt-Account-Id") != accountID {
		t.Fatalf("Chatgpt-Account-Id = %q", request.Headers.Get("Chatgpt-Account-Id"))
	}
	if request.Headers.Get("Originator") == "" || request.Headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("Codex headers = %#v", request.Headers)
	}

	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["stream"] != true || body["store"] != false {
		t.Fatalf("Codex request flags = %#v", body)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("Codex input = %#v", body["input"])
	}
	message, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("Codex message = %#v", input[0])
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) < 2 {
		t.Fatalf("Codex attachment content = %#v", message["content"])
	}
	attachment, ok := content[len(content)-1].(map[string]any)
	if !ok || attachment["type"] != "input_file" || attachment["filename"] != "report.pdf" || attachment["file_data"] != "data:application/pdf;base64,JVBERi0=" {
		t.Fatalf("Codex attachment = %#v", attachment)
	}
	artifact, _ := json.Marshal(result.Artifact)
	if strings.Contains(string(artifact), accessToken) || strings.Contains(string(artifact), "refresh") {
		t.Fatal("diagnostic artifact leaked Codex OAuth credentials")
	}
}

func TestCodexOAuthAdapterPrefersAccountIDFromAccessToken(t *testing.T) {
	accessToken := testCodexJWT(t, map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-from-jwt"},
	})
	credential := fmt.Sprintf(`{"type":"codex","access_token":%q,"refresh_token":"refresh","account_id":"account-from-document"}`, accessToken)
	channel := &dbmodel.Channel{Type: dbmodel.ChannelTypeOpenAICodex}
	outbound, err := newCodexOAuthOutbound(channel, &llm.Request{RequestType: llm.RequestTypeChat}, "https://chatgpt.com/backend-api/codex", dbmodel.ChannelKey{ChannelKey: credential})
	if err != nil {
		t.Fatalf("newCodexOAuthOutbound error: %v", err)
	}
	hello := "hello"
	request, err := outbound.TransformRequest(context.Background(), &llm.Request{RequestType: llm.RequestTypeChat, Model: "gpt-5.3-codex", Messages: []llm.Message{{Role: "user", Content: llm.MessageContent{Content: &hello}}}})
	if err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}
	if request.Headers.Get("Chatgpt-Account-Id") != "account-from-jwt" {
		t.Fatalf("Chatgpt-Account-Id = %q", request.Headers.Get("Chatgpt-Account-Id"))
	}
}

func TestCodexTokenProviderRefreshesOnceAndPreservesRotatingTokens(t *testing.T) {
	const oldRefresh = "old-refresh-secret"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Query().Get("refresh_token") != "" {
			t.Error("refresh token was placed in request URL")
		}
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected refresh request: method=%s content-type=%s", request.Method, request.Header.Get("Content-Type"))
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != oldRefresh || request.Form.Get("client_id") != codex.ClientID {
			t.Errorf("unexpected OAuth refresh form keys")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id","expires_in":3600,"token_type":"Bearer","scope":"openid profile"}`))
	}))
	t.Cleanup(server.Close)

	provider := &codexTokenProvider{
		client: server.Client(), tokenURL: server.URL,
		creds: &oauth.OAuthCredentials{AccessToken: "old-access", RefreshToken: oldRefresh, IDToken: "old-id", ExpiresAt: time.Now().Add(-time.Hour)},
	}
	const workers = 8
	results := make(chan *oauth.OAuthCredentials, workers)
	errorsCh := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			credentials, err := provider.Get(context.Background())
			results <- credentials
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
	}
	for credentials := range results {
		if credentials.AccessToken != "new-access" || credentials.RefreshToken != "new-refresh" || credentials.IDToken != "new-id" {
			t.Fatalf("refreshed credential fields were not preserved")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestCodexTokenProviderRefreshesBeforeExpiration(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	t.Cleanup(server.Close)

	provider := &codexTokenProvider{
		client: server.Client(), tokenURL: server.URL,
		creds: &oauth.OAuthCredentials{
			AccessToken: "near-expiry", RefreshToken: "refresh",
			ExpiresAt: time.Now().Add(4 * time.Minute),
		},
	}
	credentials, err := provider.ensureFresh(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("ensureFresh error: %v", err)
	}
	if credentials.AccessToken != "new-access" || calls.Load() != 1 {
		t.Fatalf("credentials=%#v refresh calls=%d", credentials, calls.Load())
	}
}

func TestCodexTokenProviderRefreshErrorDoesNotExposeResponseBody(t *testing.T) {
	const secret = "response-body-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error_description":"` + secret + `"}`))
	}))
	t.Cleanup(server.Close)
	provider := &codexTokenProvider{
		client: server.Client(), tokenURL: server.URL,
		creds: &oauth.OAuthCredentials{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)},
	}
	_, err := provider.Get(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "refresh\"") {
		t.Fatalf("refresh error was nil or exposed credential material: %v", err)
	}
}

func testCodexJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal JWT segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	return encode(map[string]any{"alg": "none", "typ": "JWT"}) + "." + encode(claims) + "."
}
