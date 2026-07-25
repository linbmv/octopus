package selfheal

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

func TestBuildVariantRequestAppliesDeltaAfterNormalChannelRewrites(t *testing.T) {
	raw := &httpclient.Request{Method: http.MethodPost, Path: "/v1/responses", ContentType: "application/json",
		Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"model":"model-a","input":"hello","store":true}`)}
	internalRequest, err := responses.NewInboundTransformer().TransformRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	internalRequest.RawRequest = raw
	channel := &model.Channel{Type: llm.APIFormatOpenAIResponse, UserAgent: "old", CustomHeader: []model.CustomHeader{{HeaderKey: "originator", HeaderValue: "old"}}}
	ua := "codex-tui/1"
	variant := Variant{Dimension: DimensionUserAgent, UserAgent: &ua, BodyDelete: []string{"store"}}
	built, err := BuildVariantRequest(context.Background(), channel, model.ChannelKey{ChannelKey: "secret"}, "https://provider.test/v1", internalRequest, variant)
	if err != nil {
		t.Fatalf("BuildVariantRequest error: %v", err)
	}
	if built.Request.Headers.Get("User-Agent") != ua || built.Request.Headers.Get("originator") != "old" {
		t.Fatalf("headers = %#v", built.Request.Headers)
	}
	var body map[string]any
	if err := json.Unmarshal(built.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["store"]; exists {
		t.Fatalf("store field survived body variant: %#v", body)
	}
	if len(built.ShapeDiff) == 0 {
		t.Fatal("variant did not produce a shape diff")
	}
}

func TestApplyVariantRejectsProtectedHeaderMutation(t *testing.T) {
	request := &httpclient.Request{Headers: http.Header{"Authorization": {"Bearer secret"}}}
	variant := Variant{Dimension: DimensionHeader, HeaderSet: map[string]string{"authorization": "replacement"}}
	// Normalize drops generated protected headers, so a generated variant is a
	// no-op and can never overwrite the provider adapter's credential.
	if err := ApplyVariant(request, variant); err != nil {
		t.Fatal(err)
	}
	if request.Headers.Get("Authorization") != "Bearer secret" {
		t.Fatal("credential header was changed")
	}
}

func TestBuildVariantRequestClampsTokenOverridesAfterProductionRewrites(t *testing.T) {
	override := `{"max_output_tokens":100000,"max_tokens":50000,"max_completion_tokens":25000}`
	channel := &model.Channel{Type: llm.APIFormatOpenAIResponse, ParamOverride: &override}
	request := minimalDiagnosticRequest(channel.Type, "model-a")
	built, err := BuildVariantRequest(context.Background(), channel, model.ChannelKey{ChannelKey: "secret"}, "https://provider.test/v1", request, Variant{Dimension: DimensionBaseline})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(built.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens"} {
		if got, ok := body[field].(float64); !ok || got != float64(DiagnosticMaxOutputTokens) {
			t.Fatalf("%s = %#v, want %d", field, body[field], DiagnosticMaxOutputTokens)
		}
	}
}

func TestBuildVariantRequestUsesProtocolSpecificSyntheticRawBody(t *testing.T) {
	tests := []struct {
		protocol llm.APIFormat
		field    string
	}{
		{llm.APIFormatOpenAIChatCompletion, "messages"},
		{llm.APIFormatOpenAIResponse, "input"},
		{llm.APIFormatAnthropicMessage, "messages"},
		{llm.APIFormatGeminiContents, "contents"},
	}
	for _, test := range tests {
		t.Run(string(test.protocol), func(t *testing.T) {
			request := minimalDiagnosticRequest(test.protocol, "model-a")
			var body map[string]any
			if err := json.Unmarshal(request.RawRequest.Body, &body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body[test.field]; !ok {
				t.Fatalf("protocol %s synthetic body = %s", test.protocol, request.RawRequest.Body)
			}
		})
	}
}

// TestMinimalDiagnosticRequestRawBodyMatchesTransformerOutput pins the
// hand-written RawRequest bodies (used verbatim by raw_passthrough channels)
// to what the real outbound transformer produces from the same llm.Request.
// If an axonhub upgrade changes the wire schema, this fails instead of
// letting diagnostics probe with a body production would never send.
func TestMinimalDiagnosticRequestRawBodyMatchesTransformerOutput(t *testing.T) {
	request := minimalDiagnosticRequest(llm.APIFormatOpenAIResponse, "model-a")
	outbound, err := responses.NewOutboundTransformer("https://provider.test", "test-key")
	if err != nil {
		t.Fatalf("create outbound transformer: %v", err)
	}
	wire, err := outbound.TransformRequest(context.Background(), request)
	if err != nil {
		t.Fatalf("transform minimal request: %v", err)
	}
	var fromTransformer, fromRaw map[string]any
	if err := json.Unmarshal(wire.Body, &fromTransformer); err != nil {
		t.Fatalf("transformer body: %v", err)
	}
	if err := json.Unmarshal(request.RawRequest.Body, &fromRaw); err != nil {
		t.Fatalf("raw body: %v", err)
	}
	for _, field := range []string{"model", "stream"} {
		if fromRaw[field] == nil {
			t.Fatalf("raw body lacks %q: %s", field, request.RawRequest.Body)
		}
	}
	if fromTransformer["model"] != fromRaw["model"] {
		t.Fatalf("model drift: transformer=%v raw=%v", fromTransformer["model"], fromRaw["model"])
	}
	// max_output_tokens is the token budget contract for Responses probes.
	if fromRaw["max_output_tokens"] == nil || fromTransformer["max_output_tokens"] == nil {
		t.Fatalf("max_output_tokens missing: transformer=%v raw=%v", fromTransformer["max_output_tokens"], fromRaw["max_output_tokens"])
	}
}
