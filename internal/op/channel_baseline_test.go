package op

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestChannelBaselineCreatePrunesPerScopeAndRoundTripsShape(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	endpoint := "https://provider.test/v1"
	artifact := requestartifact.Build(&httpclient.Request{
		Method:      http.MethodPost,
		URL:         endpoint + "/responses",
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"model":"model-a","input":"probe"}`),
	}, "openai/responses", "model-a", requestartifact.RewriteSummary{})

	for i := 0; i < model.ChannelBaselineKeepPerScope+1; i++ {
		baseline := &model.ChannelBaseline{
			ChannelID: 1, ChannelKeyID: 2, Model: "model-a", WireProtocol: "openai/responses",
			Endpoint: endpoint, EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint), ScopeFingerprint: "scope",
			RequestShape: *artifact, HTTPStatus: http.StatusOK, ContentType: "application/json",
			Source: model.ChannelBaselineSourceRelaySuccess, CapturedAt: now.Add(time.Duration(i) * time.Second), ExpiresAt: now.Add(time.Hour),
		}
		if err := ChannelBaselineCreate(ctx, baseline); err != nil {
			t.Fatalf("create baseline %d: %v", i, err)
		}
	}
	items, err := ChannelBaselineList(ctx, 1, 100)
	if err != nil {
		t.Fatalf("list baselines: %v", err)
	}
	if len(items) != model.ChannelBaselineKeepPerScope {
		t.Fatalf("baseline count = %d, want %d", len(items), model.ChannelBaselineKeepPerScope)
	}
	if items[0].RequestShape.Model != "model-a" || items[0].RequestShape.Body.Paths["/"] != "object" {
		t.Fatalf("round-tripped request shape = %#v", items[0].RequestShape)
	}
	latest, err := ChannelBaselineLatest(ctx, 1, 2, "model-a", "openai/responses", model.CapabilityEndpointFingerprint(endpoint), "scope")
	if err != nil {
		t.Fatalf("latest baseline: %v", err)
	}
	if latest.CapturedAt.Before(items[0].CapturedAt) {
		t.Fatalf("latest baseline = %v, list newest = %v", latest.CapturedAt, items[0].CapturedAt)
	}
}

func TestChannelBaselineCleanupRemovesExpiredEvidence(t *testing.T) {
	initTestDB(t)
	now := time.Now().UTC()
	endpoint := "https://provider.test"
	artifact := requestartifact.Build(&httpclient.Request{Body: []byte(`{"model":"m"}`)}, "openai/chat", "m", requestartifact.RewriteSummary{})
	baseline := &model.ChannelBaseline{
		ChannelID: 3, ChannelKeyID: 4, Model: "m", WireProtocol: "openai/chat",
		Endpoint: endpoint, EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint), ScopeFingerprint: "scope",
		RequestShape: *artifact, Source: model.ChannelBaselineSourceRelaySuccess,
		CapturedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	if err := ChannelBaselineCreate(context.Background(), baseline); err != nil {
		t.Fatalf("create expired baseline: %v", err)
	}
	if err := ChannelBaselineCleanup(context.Background(), now); err != nil {
		t.Fatalf("cleanup baselines: %v", err)
	}
	items, err := ChannelBaselineList(context.Background(), 3, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("expired baselines = %#v, err=%v", items, err)
	}
}
