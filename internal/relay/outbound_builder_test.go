package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestBuildFinalOutboundRequestUsesProductionTransformerAndRewritesWithoutNetwork(t *testing.T) {
	raw := &httpclient.Request{
		Method: http.MethodPost, Path: "/v1/responses", ContentType: "application/json",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"model":"model-a","input":"hello","stream":false}`),
	}
	inbound := newInbound(llm.APIFormatOpenAIResponse)
	internalRequest, err := inbound.TransformRequest(context.Background(), raw)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	internalRequest.RawRequest = raw
	channel := &dbmodel.Channel{
		Type: llm.APIFormatOpenAIResponse, UserAgent: "codex-tui/1", CustomHeader: []dbmodel.CustomHeader{{HeaderKey: "originator", HeaderValue: "codex-tui"}},
		ParamOverride: stringPointerForBuilder(`{"store":false}`),
	}
	result, err := BuildFinalOutboundRequest(context.Background(), channel, dbmodel.ChannelKey{ChannelKey: "secret-key"}, "https://provider.test/v1", internalRequest)
	if err != nil {
		t.Fatalf("BuildFinalOutboundRequest error: %v", err)
	}
	if result.Request.Headers.Get("User-Agent") != "codex-tui/1" || result.Request.Headers.Get("originator") != "codex-tui" {
		t.Fatalf("final headers = %#v", result.Request.Headers)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Request.Body, &body); err != nil || body["store"] != false {
		t.Fatalf("final body = %s, err=%v", result.Request.Body, err)
	}
	encoded, _ := json.Marshal(result.Artifact)
	if strings.Contains(string(encoded), "secret-key") {
		t.Fatalf("artifact leaked authentication: %s", encoded)
	}
	if value, exists := result.Artifact.Headers["authorization"]; exists && value != "[redacted]" {
		t.Fatalf("artifact retained Authorization value: %s", encoded)
	}
	if !result.Artifact.Rewrite.ParamOverrideApplied || !result.Artifact.Rewrite.HeaderRewriteApplied {
		t.Fatalf("rewrite summary = %#v", result.Artifact.Rewrite)
	}
}

func stringPointerForBuilder(value string) *string { return &value }
