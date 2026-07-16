package capability

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestFakeProviderTextWorksButToolsNotImplemented(t *testing.T) {
	provider := fakeProbeProvider(t, func(request *http.Request, body string) *http.Response {
		if strings.Contains(body, `"tools"`) {
			return probeHTTPResponse(http.StatusNotImplemented, `{"error":{"message":"tools are not implemented"}}`, "application/json")
		}
		return probeHTTPResponse(http.StatusOK, `{"choices":[{"message":{"content":"OK"}}]}`, "application/json")
	})
	channel, key := probeChannelFixture()

	text := provider.Probe(context.Background(), channel, key, "https://provider.test", "listed-model", model.CapabilityText, 8)
	if text.Status != model.CapabilitySupported {
		t.Fatalf("text status = %q, want supported", text.Status)
	}
	tools := provider.Probe(context.Background(), channel, key, "https://provider.test", "listed-model", model.CapabilityTool, 8)
	if tools.Status != model.CapabilityNotImplemented || tools.ErrorClass != "not_implemented" {
		t.Fatalf("tool result = %#v, want not_implemented", tools)
	}
}

func TestFakeProviderListedModelCanStillBeUnauthorized(t *testing.T) {
	listedModels := map[string]bool{"listed-model": true}
	provider := fakeProbeProvider(t, func(_ *http.Request, _ string) *http.Response {
		return probeHTTPResponse(http.StatusForbidden, `{"error":{"message":"account does not have access to this model"}}`, "application/json")
	})
	channel, key := probeChannelFixture()
	if !listedModels["listed-model"] {
		t.Fatal("fake provider fixture must expose the model in its directory")
	}
	result := provider.Probe(context.Background(), channel, key, "https://provider.test", "listed-model", model.CapabilityText, 8)
	if result.Status != model.CapabilityUnauthorized {
		t.Fatalf("listed but unauthorized result = %#v", result)
	}
}

func TestFakeProviderToolCallSucceeds(t *testing.T) {
	provider := fakeProbeProvider(t, func(_ *http.Request, body string) *http.Response {
		if !strings.Contains(body, "octopus_capability_probe") {
			t.Fatal("probe did not force the sentinel tool")
		}
		return probeHTTPResponse(http.StatusOK, `{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"octopus_capability_probe","arguments":"{}"}}]}}]}`, "application/json")
	})
	channel, key := probeChannelFixture()
	result := provider.Probe(context.Background(), channel, key, "https://provider.test", "listed-model", model.CapabilityTool, 8)
	if result.Status != model.CapabilitySupported {
		t.Fatalf("tool result = %#v, want supported", result)
	}
}

func TestStreamProbeRequiresStreamingResponseEvidence(t *testing.T) {
	provider := fakeProbeProvider(t, func(_ *http.Request, _ string) *http.Response {
		return probeHTTPResponse(http.StatusOK, `{"choices":[{"message":{"content":"OK"}}]}`, "application/json")
	})
	channel, key := probeChannelFixture()
	result := provider.Probe(context.Background(), channel, key, "https://provider.test", "listed-model", model.CapabilityStream, 8)
	if result.Status != model.CapabilityUnsupported || result.ErrorClass != "non_stream_response" {
		t.Fatalf("stream result = %#v, want unsupported non-stream response", result)
	}
}

func fakeProbeProvider(t *testing.T, handler func(*http.Request, string) *http.Response) HTTPProber {
	t.Helper()
	return HTTPProber{ClientForChannel: func(*model.Channel) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read fake provider request: %v", err)
			}
			if request.URL.Path != "/v1/chat/completions" {
				t.Fatalf("probe path = %q, want actual OpenAI wire endpoint", request.URL.Path)
			}
			if request.Header.Get("Authorization") != "Bearer account-key" {
				t.Fatal("probe did not use the selected account scope")
			}
			response := handler(request, string(body))
			response.Request = request
			return response, nil
		})}, nil
	}}
}

func probeChannelFixture() (*model.Channel, model.ChannelKey) {
	key := model.ChannelKey{ID: 7, ChannelID: 3, Enabled: true, ChannelKey: "account-key"}
	return &model.Channel{
		ID: 3, Type: llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test"}}, Keys: []model.ChannelKey{key},
	}, key
}

func probeHTTPResponse(status int, body, contentType string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
