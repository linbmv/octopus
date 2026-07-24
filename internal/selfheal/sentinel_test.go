package selfheal

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm/httpclient"
)

func createSentinelBaseline(t *testing.T, channel model.Channel) {
	t.Helper()
	key := channel.Keys[0]
	endpoint := channel.BaseUrls[0].URL
	now := time.Now().UTC()
	artifact := requestartifact.Build(&httpclient.Request{
		Method: "POST", URL: endpoint, Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{"model":"model-a","input":"Reply with OK."}`),
	}, string(channel.Type), channel.Model, requestartifact.RewriteSummary{})
	if err := op.ChannelBaselineCreate(context.Background(), &model.ChannelBaseline{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: channel.Model, WireProtocol: channel.Type,
		Endpoint: endpoint, EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint),
		ScopeFingerprint: model.CapabilityScopeFingerprint(&channel, key, endpoint), RequestShape: *artifact,
		HTTPStatus: http.StatusOK, ContentType: "application/json", Source: model.ChannelBaselineSourceRelaySuccess,
		CapturedAt: now, ExpiresAt: now.Add(time.Hour), Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSentinelUsesFixedScopeAndRequiresSuccessfulBaseline(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	config := testSelfHealingConfig()
	config.FailureThreshold = 3
	config.FailureWindowSeconds = 300
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionUserAgent}
	worker, err := NewWorker(config, fake)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = worker.Stop(stopCtx)
	}()

	now := time.Now().UTC()
	observation := relay.UpstreamFailureObservation{
		ChannelID: channel.ID, ChannelKeyID: channel.Keys[0].ID, Model: channel.Model,
		Endpoint: channel.BaseUrls[0].URL, HTTPStatus: http.StatusBadRequest,
		Headers:      http.Header{"Content-Type": {"application/json"}},
		ResponseBody: []byte(`{"error":{"type":"validation_error","message":"unknown field"}}`), ObservedAt: now,
	}
	for i := 0; i < config.FailureThreshold; i++ {
		observation.ObservedAt = now.Add(time.Duration(i) * time.Second)
		worker.sentinel.Observe(observation)
	}
	time.Sleep(50 * time.Millisecond)
	sessions, err := op.DiagnosticSessionList(context.Background(), channel.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sentinel created a session without a baseline: %#v", sessions)
	}

	// A fresh worker avoids the deliberate per-scope cooldown from the rejected
	// trigger, then proves that the exact successful scope unlocks diagnosis.
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	createSentinelBaseline(t, channel)
	worker, err = NewWorker(config, fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < config.FailureThreshold; i++ {
		observation.ObservedAt = now.Add(time.Duration(i+10) * time.Second)
		worker.sentinel.Observe(observation)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sessions, err = op.DiagnosticSessionList(context.Background(), channel.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sentinel did not create a diagnostic session for a baseline-backed scope")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sessions[0].Trigger != model.DiagnosticTriggerSentinel || sessions[0].ChannelKeyID != channel.Keys[0].ID ||
		sessions[0].Model != channel.Model || sessions[0].EndpointFingerprint != model.CapabilityEndpointFingerprint(channel.BaseUrls[0].URL) {
		t.Fatalf("sentinel diagnostic scope = %#v", sessions[0])
	}
}

func TestSentinelCapacityFailureDoesNotCreateDiagnosticTraffic(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	config := testSelfHealingConfig()
	config.FailureThreshold = 1
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionUserAgent}
	worker, err := NewWorker(config, fake)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()
	worker.sentinel.Observe(relay.UpstreamFailureObservation{
		ChannelID: channel.ID, ChannelKeyID: channel.Keys[0].ID, Model: channel.Model,
		Endpoint: channel.BaseUrls[0].URL, HTTPStatus: http.StatusServiceUnavailable,
		Headers: http.Header{"Content-Type": {"application/json"}}, ResponseBody: []byte(`{"error":{"type":"overloaded_error"}}`),
	})
	time.Sleep(50 * time.Millisecond)
	sessions, err := op.DiagnosticSessionList(context.Background(), channel.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("capacity failure created diagnostic session: %#v", sessions)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Fatalf("capacity failure created %d diagnostic requests", len(fake.calls))
	}
}

func TestDiagnosticScopeAndSyntheticTokenBudgetAreBounded(t *testing.T) {
	channel := model.Channel{
		ID: 7, Type: "openai/responses", Model: "model-a,model-b", CustomModel: "custom-a",
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test/v1"}},
		Keys:     []model.ChannelKey{{ID: 11, ChannelID: 7, Enabled: true, ChannelKey: "stored-secret"}},
	}
	if _, err := selectDiagnosticKey(&channel, 999); err == nil {
		t.Fatal("diagnostic selected a key outside the channel")
	}
	if _, err := selectDiagnosticEndpoint(&channel, "https://attacker.test"); err == nil {
		t.Fatal("diagnostic accepted an unconfigured endpoint")
	}
	if _, err := selectDiagnosticModel(&channel, "attacker-model"); err == nil {
		t.Fatal("diagnostic accepted an unconfigured model")
	}
	request := minimalDiagnosticRequest(channel.Type, "model-b")
	if request.Model != "model-b" || request.MaxTokens == nil || *request.MaxTokens != 8 || request.RawRequest == nil {
		t.Fatalf("synthetic diagnostic request = %#v", request)
	}
	if string(request.RawRequest.Body) != `{"model":"model-b","input":"Reply with OK.","max_output_tokens":8,"stream":false}` {
		t.Fatalf("synthetic diagnostic body = %s", request.RawRequest.Body)
	}
	if string(request.RawRequest.Body) == "stored-secret" {
		t.Fatal("synthetic request body used the channel token")
	}
}
