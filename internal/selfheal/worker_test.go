package selfheal

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

type fakeDiagnosticExecutor struct {
	mu        sync.Mutex
	calls     []Variant
	succeedOn string
}

func (f *fakeDiagnosticExecutor) Execute(_ context.Context, _ *model.Channel, _ model.ChannelKey, _ string, _ *llm.Request, variant Variant) AttemptResult {
	f.mu.Lock()
	f.calls = append(f.calls, variant)
	f.mu.Unlock()
	artifact := requestartifact.Build(&httpclient.Request{Body: []byte(`{"model":"model-a","input":"probe"}`)}, "openai/responses", "model-a", requestartifact.RewriteSummary{})
	if variant.Dimension == f.succeedOn {
		return AttemptResult{Artifact: artifact, Classification: Diagnosis{RootCause: model.RootCauseNone}, Success: true, HTTPStatus: 200, ResponseHeaders: map[string][]string{"content-type": {"application/json"}}, ResponseFingerprint: "success", Duration: time.Millisecond}
	}
	return AttemptResult{Artifact: artifact, Classification: Diagnosis{Classification: errorclass.Classification{Level: errorclass.ErrorLevelChannel, Reason: "schema drift"}, RootCause: model.RootCauseProtocolDrift}, HTTPStatus: 400, ResponseHeaders: map[string][]string{"content-type": {"application/json"}}, ResponseFingerprint: "failure", Duration: time.Millisecond}
}

func initSelfHealingWorkerDB(t *testing.T) model.Channel {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "self-healing-worker.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "self-healing-worker", Type: llm.APIFormatOpenAIResponse, Enabled: true,
		SelfHealingEnabled: true, BaseUrls: []model.BaseUrl{{URL: "https://provider.test/v1"}}, Model: "model-a",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "diagnostic-key"}}}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatal(err)
	}
	return channel
}

func testSelfHealingConfig() conf.SelfHealing {
	config := conf.Default().SelfHealing
	config.Enabled = true
	config.Diagnostic.MaxVariants = 4
	config.Diagnostic.MaxConcurrency = 1
	config.Diagnostic.QueueDepth = 4
	config.Diagnostic.RequestsPerMinute = 6000
	config.Diagnostic.TimeoutSeconds = 2
	config.Diagnostic.SessionTTLSeconds = 30
	config.Diagnostic.CostPerRequestUSD = 0.001
	config.Diagnostic.MaxBatchCostUSD = 0.01
	config.Diagnostic.MaxTotalCostUSD = 0.05
	return config
}

func TestWorkerRunsBoundedIndependentDiagnosticSession(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionUserAgent}
	worker, err := NewWorker(testSelfHealingConfig(), fake)
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
	report, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift})
	if err != nil || !report.Accepted || report.Session == nil {
		t.Fatalf("submit report = %#v, err=%v", report, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		session, getErr := op.DiagnosticSessionGet(context.Background(), report.Session.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if session.Status == model.DiagnosticSessionCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session did not complete: %#v", session)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fake.mu.Lock()
	callCount := len(fake.calls)
	fake.mu.Unlock()
	if callCount == 0 || callCount > 4 {
		t.Fatalf("diagnostic calls = %d, want 1..4", callCount)
	}
}

func TestWorkerStopsCapacityBeforeCreatingLiveJobs(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionUserAgent}
	worker, err := NewWorker(testSelfHealingConfig(), fake)
	if err != nil {
		t.Fatal(err)
	}
	report, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseCapacity})
	if err != nil || !report.EarlyStop || report.Accepted || report.ReservedCostUSD != 0 {
		t.Fatalf("capacity report = %#v, err=%v", report, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Fatalf("capacity early stop created %d live jobs", len(fake.calls))
	}
}

func TestWorkerPreviewDoesNotUseExecutorOrNetwork(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionUserAgent}
	worker, err := NewWorker(testSelfHealingConfig(), fake)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := worker.Preview(context.Background(), PreviewRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift, MaxVariants: 2})
	if err != nil || preview == nil || len(preview.Artifacts) != 2 {
		t.Fatalf("preview = %#v, err=%v", preview, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Fatalf("preview invoked live executor: %d calls", len(fake.calls))
	}
}

func TestWorkerRequiresPriorSuccessfulBaselineBeforeCreatingPatchAndReleasesBudget(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	config := testSelfHealingConfig()
	config.Diagnostic.MaxTotalCostUSD = config.Diagnostic.MaxBatchCostUSD
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

	waitCompleted := func(sessionID string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			session, getErr := op.DiagnosticSessionGet(context.Background(), sessionID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if session.Status == model.DiagnosticSessionCompleted {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s did not complete", sessionID)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	first, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift})
	if err != nil {
		t.Fatal(err)
	}
	waitCompleted(first.Session.ID)
	patches, err := op.ChannelPatchListBySession(context.Background(), first.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 0 {
		t.Fatalf("first diagnostic without a prior baseline created patches: %#v", patches)
	}

	// The first verified variant becomes a baseline. The second session must be
	// accepted despite the tight total budget (the first reservation was
	// released), and can now produce a candidate patch.
	second, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift})
	if err != nil {
		t.Fatalf("second submit after budget release: %v", err)
	}
	waitCompleted(second.Session.ID)
	patches, err = op.ChannelPatchListBySession(context.Background(), second.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].Status != model.ChannelPatchPreviewed {
		t.Fatalf("baseline-backed diagnostic patches = %#v", patches)
	}
}

func TestWorkerDisabledAndChannelGate(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	config := testSelfHealingConfig()
	config.Enabled = false
	worker, err := NewWorker(config, &fakeDiagnosticExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift}); err != ErrSelfHealingDisabled {
		t.Fatalf("disabled worker error = %v", err)
	}
	config.Enabled = true
	worker, err = NewWorker(config, &fakeDiagnosticExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	channel.SelfHealingEnabled = false
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, SelfHealingEnabled: boolPointer(false)}, context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: channel.ID, RootCause: model.RootCauseProtocolDrift}); err != ErrChannelSelfHealingDisabled {
		t.Fatalf("channel gate error = %v", err)
	}
}

func boolPointer(value bool) *bool { return &value }
