package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestCompactStrategyDoesNotTryChatAfterOfficialFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			t.Fatalf("Compact official failure must not call non-compact Responses endpoint")
		case "/v1/chat/completions":
			t.Fatalf("Compact official failure must not call Chat endpoint")
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "anyrouter-codex",
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

	_, _, _, err = ra.forward()
	if err == nil {
		t.Fatal("official compact failure must not call Chat or succeed")
	}

	want := []string{"/v1/responses/compact"}
	if len(paths) != len(want) {
		t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
		}
	}
}

func TestCompactStrategyStopsAfterOfficialFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			t.Fatalf("Compact official failure must not call non-compact Responses endpoint")
		case "/v1/chat/completions":
			t.Fatalf("Compact official failure must not call Chat endpoint")
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "responses-cache",
		Type:     llm.APIFormatOpenAIResponse,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}},
	}

	_, err := runCompactForwardForTest(t, channel)
	if err == nil {
		t.Fatal("official compact failure should fail instead of calling non-compact Responses")
	}

	want := []string{"/v1/responses/compact"}
	if len(paths) != len(want) {
		t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
		}
	}
}

func TestPersistedObsoleteCompactStrategyDoesNotSkipOfficial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/responses/compact":
			_, _ = w.Write([]byte(compactResponseFixture()))
		case "/v1/responses":
			t.Fatalf("persisted obsolete compact strategy must not call non-compact Responses endpoint")
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "persisted-responses",
		Type:     llm.APIFormatOpenAIResponse,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}},
	}
	groupItem := dbmodel.GroupItem{
		ID:              1,
		ChannelID:       channel.ID,
		ModelName:       "gpt-5.5",
		CompactStrategy: dbmodel.CompactStrategy("obsolete"),
	}

	statusCode, err := runCompactForwardForTestWithItem(t, channel, groupItem)
	if err != nil {
		t.Fatalf("forward returned error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, 期望 200", statusCode)
	}

	want := []string{"/v1/responses/compact"}
	if len(paths) != len(want) {
		t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
		}
	}
}

func TestCompactStrategyDoesNotCallChatEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid codex request (request id: x)","code":"invalid_responses_request","type":"new_api_error"}}`))
		case "/v1/chat/completions":
			t.Fatalf("Compact official failure must not call Chat endpoint")
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "chat-cache",
		Type:     llm.APIFormatOpenAIResponse,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstream.URL + "/v1"}},
	}

	_, err := runCompactForwardForTest(t, channel)
	if err == nil {
		t.Fatal("official compact failure should fail instead of calling Chat")
	}

	want := []string{"/v1/responses/compact"}
	if len(paths) != len(want) {
		t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("upstream paths = %#v, 期望 %#v", paths, want)
		}
	}
}

func runCompactForwardForTest(t *testing.T, channel *dbmodel.Channel) (int, error) {
	t.Helper()
	return runCompactForwardForTestWithItem(t, channel, dbmodel.GroupItem{
		ID:        1,
		ChannelID: channel.ID,
		ModelName: "gpt-5.5",
	})
}

func runCompactForwardForTestWithItem(t *testing.T, channel *dbmodel.Channel, groupItem dbmodel.GroupItem) (int, error) {
	t.Helper()

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
		groupItem:  groupItem,
		usedKey:    dbmodel.ChannelKey{ID: 1, ChannelKey: "test-key"},
	}

	statusCode, _, _, err := ra.forward()
	return statusCode, err
}
