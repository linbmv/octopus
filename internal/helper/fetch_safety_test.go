package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/looplj/axonhub/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackedBody struct {
	reader io.Reader
	closed atomic.Bool
}

func newTrackedBody(content string) *trackedBody {
	return &trackedBody{reader: strings.NewReader(content)}
}

func (b *trackedBody) Read(buffer []byte) (int, error) {
	return b.reader.Read(buffer)
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestApplyCustomHeadersProtectsAuthentication(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer provider-owned")
	applyCustomHeaders(request, model.Channel{CustomHeader: []model.CustomHeader{
		{HeaderKey: "Authorization", HeaderValue: "Bearer channel-owned"},
		{HeaderKey: "X-Trace", HeaderValue: "audit"},
	}})
	if got := request.Header.Get("Authorization"); got != "Bearer provider-owned" {
		t.Fatalf("Authorization = %q, want provider-owned value", got)
	}
	if got := request.Header.Get("X-Trace"); got != "audit" {
		t.Fatalf("X-Trace = %q, want audit", got)
	}
}

func TestModelPaginationClosesEachBodyBeforeNextRequest(t *testing.T) {
	tests := []struct {
		name    string
		payload func(int) string
		fetch   func(*http.Client, model.Channel) ([]string, error)
	}{
		{
			name: "Gemini",
			payload: func(call int) string {
				if call == 1 {
					return `{"models":[{"name":"models/first"}],"nextPageToken":"next"}`
				}
				return `{"models":[{"name":"models/second"}]}`
			},
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchGeminiModels(client, context.Background(), channel, "key")
			},
		},
		{
			name: "Anthropic",
			payload: func(call int) string {
				if call == 1 {
					return `{"data":[{"id":"first"}],"has_more":true,"last_id":"next"}`
				}
				return `{"data":[{"id":"second"}],"has_more":false}`
			},
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchAnthropicModels(client, context.Background(), channel, "key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodies []*trackedBody
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if len(bodies) > 0 && !bodies[len(bodies)-1].closed.Load() {
					return nil, fmt.Errorf("previous response body was not closed before next request")
				}
				body := newTrackedBody(tt.payload(len(bodies) + 1))
				bodies = append(bodies, body)
				return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
			})}

			models, err := tt.fetch(client, paginationTestChannel())
			if err != nil {
				t.Fatalf("fetch returned error: %v", err)
			}
			if len(models) != 2 {
				t.Fatalf("models = %v, want two pages", models)
			}
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want 2", len(bodies))
			}
			for i, body := range bodies {
				if !body.closed.Load() {
					t.Fatalf("body %d was not closed", i+1)
				}
			}
		})
	}
}

func TestDecodeModelListPageRejectsOversizedResponseAndClosesBody(t *testing.T) {
	body := newTrackedBody(`{"data":[]}`)
	response := &http.Response{
		StatusCode:    http.StatusOK,
		Body:          body,
		ContentLength: bodylimit.DefaultMetadataResponseBytes + 1,
	}
	var target model.OpenAIModelList
	err := decodeModelListPage(response, "OpenAI", &target, newModelFetchBudget(maxModelListResponseBytes))
	if !errors.Is(err, bodylimit.ErrTooLarge) {
		t.Fatalf("decodeModelListPage() error = %v, want body limit error", err)
	}
	if !body.closed.Load() {
		t.Fatal("oversized model response body was not closed")
	}
}

func TestModelDiscoveryEnforcesCumulativeResponseBudgetAcrossPages(t *testing.T) {
	budget := newModelFetchBudget(15)
	for page, payload := range []string{`{"data":[]}`, `{"data":[]}`} {
		response := &http.Response{
			StatusCode:    http.StatusOK,
			Body:          newTrackedBody(payload),
			ContentLength: int64(len(payload)),
		}
		var target model.OpenAIModelList
		err := decodeModelListPage(response, "OpenAI", &target, budget)
		if page == 0 && err != nil {
			t.Fatalf("first page error = %v", err)
		}
		if page == 1 && !errors.Is(err, errModelListResourceLimit) {
			t.Fatalf("second page error = %v, want cumulative budget error", err)
		}
	}
}

func TestModelAccumulatorEnforcesUniqueModelLimit(t *testing.T) {
	accumulator := newModelAccumulator(2)
	for _, name := range []string{"first", "first", "second"} {
		if err := accumulator.add(name); err != nil {
			t.Fatalf("add(%q) error = %v", name, err)
		}
	}
	if err := accumulator.add("third"); !errors.Is(err, errModelListResourceLimit) {
		t.Fatalf("third unique model error = %v, want resource limit", err)
	}
}

func TestModelPaginationRejectsEmptyAndRepeatedTokens(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantCalls int
		fetch     func(*http.Client, model.Channel) ([]string, error)
	}{
		{
			name:      "Gemini empty token",
			payload:   `{"nextPageToken":"   "}`,
			wantCalls: 1,
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchGeminiModels(client, context.Background(), channel, "key")
			},
		},
		{
			name:      "Gemini repeated token",
			payload:   `{"nextPageToken":"same"}`,
			wantCalls: 2,
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchGeminiModels(client, context.Background(), channel, "key")
			},
		},
		{
			name:      "Anthropic empty token",
			payload:   `{"has_more":true,"last_id":""}`,
			wantCalls: 1,
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchAnthropicModels(client, context.Background(), channel, "key")
			},
		},
		{
			name:      "Anthropic repeated token",
			payload:   `{"has_more":true,"last_id":"same"}`,
			wantCalls: 2,
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchAnthropicModels(client, context.Background(), channel, "key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Body: newTrackedBody(tt.payload)}, nil
			})}
			_, err := tt.fetch(client, paginationTestChannel())
			if err == nil {
				t.Fatal("fetch error = nil, want pagination progress error")
			}
			if calls != tt.wantCalls {
				t.Fatalf("requests = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestFetchModelsWithKeyDoesNotHidePaginationErrorBehindEmptyFallback(t *testing.T) {
	geminiCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload string
		switch request.URL.Path {
		case "/v1beta/models":
			geminiCalls++
			payload = `{"nextPageToken":"same"}`
		case "/v1/models":
			payload = `{}`
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: newTrackedBody(payload)}, nil
	})}
	channel := paginationTestChannel()

	_, err := fetchModelsWithKey(client, context.Background(), channel, "key")
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("fetch error = %v, want repeated token error", err)
	}
	if geminiCalls != 2 {
		t.Fatalf("Gemini requests = %d, want 2", geminiCalls)
	}
}

func TestModelPaginationStopsAtMaximumPageCount(t *testing.T) {
	tests := []struct {
		name    string
		payload func(int) string
		fetch   func(*http.Client, model.Channel) ([]string, error)
	}{
		{
			name: "Gemini",
			payload: func(call int) string {
				return fmt.Sprintf(`{"nextPageToken":"token-%d"}`, call)
			},
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchGeminiModels(client, context.Background(), channel, "key")
			},
		},
		{
			name: "Anthropic",
			payload: func(call int) string {
				return fmt.Sprintf(`{"has_more":true,"last_id":"token-%d"}`, call)
			},
			fetch: func(client *http.Client, channel model.Channel) ([]string, error) {
				return fetchAnthropicModels(client, context.Background(), channel, "key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Body: newTrackedBody(tt.payload(calls))}, nil
			})}

			_, err := tt.fetch(client, paginationTestChannel())
			if err == nil || !strings.Contains(err.Error(), "exceeded") {
				t.Fatalf("fetch error = %v, want maximum page error", err)
			}
			if calls != maxModelListPages {
				t.Fatalf("requests = %d, want %d", calls, maxModelListPages)
			}
		})
	}
}

func paginationTestChannel() model.Channel {
	return model.Channel{
		Type:     llm.APIFormatGeminiContents,
		BaseUrls: []model.BaseUrl{{URL: "https://models.example"}},
	}
}
