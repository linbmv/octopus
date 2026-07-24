package selfheal

import (
	"errors"
	"net/http"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

func TestClassifyPreservesErrorLevelAndAddsRootCause(t *testing.T) {
	tests := []struct {
		name        string
		observation Observation
		wantLevel   errorclass.ErrorLevel
		wantCause   model.RootCause
		patchable   bool
	}{
		{name: "success", observation: Observation{HTTPStatus: 200, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"id":"ok"}`)}, wantLevel: errorclass.ErrorLevelNone, wantCause: model.RootCauseNone},
		{name: "global rate limit", observation: Observation{HTTPStatus: 429, Headers: http.Header{"Retry-After": {"120"}}}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseRateLimit},
		{name: "key quota", observation: Observation{HTTPStatus: 200, Body: []byte(`{"error":{"type":"1308"}}`)}, wantLevel: errorclass.ErrorLevelKey, wantCause: model.RootCauseRateLimit},
		{name: "auth", observation: Observation{HTTPStatus: 401, Body: []byte(`{"error":"invalid api key"}`)}, wantLevel: errorclass.ErrorLevelKey, wantCause: model.RootCauseAuth},
		{name: "waf forbidden", observation: Observation{HTTPStatus: 403, Headers: http.Header{"Content-Type": {"text/html"}}, Body: []byte(`<!doctype html><html>Cloudflare challenge</html>`)}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseWAFOrClientFingerprint, patchable: true},
		{name: "waf behind 200", observation: Observation{HTTPStatus: 200, Headers: http.Header{"Content-Type": {"text/html"}}, Body: []byte(`<!doctype html><html>enable javascript</html>`)}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseWAFOrClientFingerprint, patchable: true},
		{name: "schema drift", observation: Observation{HTTPStatus: 400, Body: []byte(`{"error":{"type":"invalid_request_error","message":"unknown field reasoning"}}`)}, wantLevel: errorclass.ErrorLevelClient, wantCause: model.RootCauseProtocolDrift, patchable: true},
		{name: "endpoint", observation: Observation{HTTPStatus: 404, Body: []byte(`not found`)}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseEndpoint},
		{name: "model access", observation: Observation{HTTPStatus: 404, Body: []byte(`{"error":"model_not_found"}`)}, wantLevel: errorclass.ErrorLevelClient, wantCause: model.RootCauseModelAccess},
		{name: "capacity", observation: Observation{HTTPStatus: 503, Body: []byte(`model overloaded`)}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseCapacity},
		{name: "sse capacity", observation: Observation{HTTPStatus: 200, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: []byte("event: error\ndata: {\"error\":{\"type\":\"overloaded_error\"}}\n\n")}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseCapacity},
		{name: "decode", observation: Observation{HTTPStatus: 200, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"id":"ok"}`), DecodeError: errors.New("invalid utf-8")}, wantLevel: errorclass.ErrorLevelNone, wantCause: model.RootCauseDecode},
		{name: "network", observation: Observation{TransportError: errors.New("dial tcp: connection refused")}, wantLevel: errorclass.ErrorLevelChannel, wantCause: model.RootCauseNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.observation)
			if got.Classification.Level != test.wantLevel || got.RootCause != test.wantCause || got.PatchEligible() != test.patchable {
				t.Fatalf("Classify() = level=%s cause=%s patchable=%t, want level=%s cause=%s patchable=%t",
					got.Classification.Level.String(), got.RootCause, got.PatchEligible(), test.wantLevel.String(), test.wantCause, test.patchable)
			}
		})
	}
}

func TestClassifyBoundsTransportReason(t *testing.T) {
	got := Classify(Observation{TransportError: errors.New("dial\r\n" + string(make([]byte, 400)))})
	if len([]rune(got.Classification.Reason)) > 259 {
		t.Fatalf("reason length = %d, want bounded", len([]rune(got.Classification.Reason)))
	}
}
