package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestResponseLimitSurvivesAxonHTTPClientWrapping(t *testing.T) {
	for _, test := range []struct {
		name          string
		contentLength int64
		body          string
	}{
		{name: "declared oversized", contentLength: 5, body: "x"},
		{name: "chunked oversized", contentLength: -1, body: "12345"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &relayTrackedBody{Reader: strings.NewReader(test.body)}
			base := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          body,
					ContentLength: test.contentLength,
				}, nil
			})
			client := &http.Client{Transport: base}
			limited := httpClientWithResponseLimit(client, 4, true, nil, nil)
			axonClient := httpclient.NewHttpClientWithClient(limited)
			_, err := axonClient.Do(context.Background(), &httpclient.Request{
				Method:  http.MethodGet,
				URL:     "https://upstream.example/response",
				Headers: make(http.Header),
			})
			if !errors.Is(err, bodylimit.ErrTooLarge) {
				t.Fatalf("axon client error = %v, want body limit sentinel", err)
			}
			if !body.closed.Load() {
				t.Fatal("rejected upstream response body was not closed")
			}
		})
	}
}

func TestStreamingResponseLimitOnlyCapsErrorsAndReportsRawActivity(t *testing.T) {
	var activity atomic.Int64
	successBody := &relayTrackedBody{Reader: strings.NewReader("unlimited-stream")}
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": {"text/event-stream"}},
			Body:          successBody,
			ContentLength: int64(len("unlimited-stream")),
		}, nil
	})
	client := httpClientWithResponseLimit(&http.Client{Transport: base}, 4, false, func() { activity.Add(1) }, nil)
	response, err := client.Get("https://upstream.example/stream")
	if err != nil {
		t.Fatalf("stream GET error = %v", err)
	}
	data, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close stream response: %v", closeErr)
	}
	if err != nil || string(data) != "unlimited-stream" {
		t.Fatalf("successful stream read = (%q, %v)", data, err)
	}
	if activity.Load() == 0 {
		t.Fatal("successful raw stream reads did not report activity")
	}
}

func TestStreamingHTTPErrorBodyRemainsBounded(t *testing.T) {
	body := &relayTrackedBody{Reader: strings.NewReader("12345")}
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusBadGateway,
			Status:        "502 Bad Gateway",
			Header:        make(http.Header),
			Body:          body,
			ContentLength: -1,
		}, nil
	})
	limited := httpClientWithResponseLimit(&http.Client{Transport: base}, 4, false, nil, nil)
	axonClient := httpclient.NewHttpClientWithClient(limited)
	_, err := axonClient.DoStream(context.Background(), &httpclient.Request{
		Method:  http.MethodGet,
		URL:     "https://upstream.example/stream",
		Headers: make(http.Header),
	})
	if !errors.Is(err, bodylimit.ErrTooLarge) {
		t.Fatalf("stream error body = %v, want body limit sentinel", err)
	}
	if !body.closed.Load() {
		t.Fatal("oversized streaming error body was not closed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type relayTrackedBody struct {
	io.Reader
	closed atomic.Bool
	reads  atomic.Int64
}

func (b *relayTrackedBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.Reader.Read(p)
}

func (b *relayTrackedBody) Close() error {
	b.closed.Store(true)
	return nil
}
