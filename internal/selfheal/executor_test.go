package selfheal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

func diagnosticRequest(t *testing.T) *llm.Request {
	t.Helper()
	raw := &httpclient.Request{Method: http.MethodPost, Path: "/v1/responses", ContentType: "application/json",
		Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"model":"model-a","input":"Reply with OK","stream":false}`)}
	request, err := responses.NewInboundTransformer().TransformRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	request.RawRequest = raw
	return request
}

func TestIsolatedExecutorDoesNotEnterProductionRelayState(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "" {
			t.Error("diagnostic request did not carry provider authentication")
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><html>Cloudflare challenge</html>`))
	}))
	defer server.Close()
	channel := &model.Channel{ID: 3, Type: llm.APIFormatOpenAIResponse, UserAgent: "codex-cli"}
	result := (IsolatedExecutor{Now: func() time.Time { return time.Now() }}).Execute(context.Background(), channel,
		model.ChannelKey{ID: 4, ChannelID: 3, ChannelKey: "secret"}, server.URL, diagnosticRequest(t), Variant{Dimension: DimensionBaseline})
	if requests.Load() != 1 || result.HTTPStatus != http.StatusForbidden || result.Classification.RootCause != model.RootCauseWAFOrClientFingerprint || result.Classification.Classification.Level != errorclass.ErrorLevelChannel {
		t.Fatalf("isolated result = %#v, requests=%d", result, requests.Load())
	}
	if result.Success || result.ResponseFingerprint == "" || result.Artifact == nil {
		t.Fatalf("diagnostic result missing bounded evidence: %#v", result)
	}
}

func TestIsolatedExecutorStopsAfterFirstValidSSEEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response\ndata: {\"id\":\"ok\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := (IsolatedExecutor{}).Execute(ctx, &model.Channel{ID: 4, Type: llm.APIFormatOpenAIResponse},
		model.ChannelKey{ID: 5, ChannelID: 4, ChannelKey: "secret"}, server.URL, diagnosticRequest(t), Variant{Dimension: DimensionBaseline})
	if !result.Success || result.Classification.RootCause != model.RootCauseNone {
		t.Fatalf("SSE diagnostic result = %#v", result)
	}
}
