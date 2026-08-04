package selfheal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/routingstate"
)

func createApplyPatch(t *testing.T, channelID int, nextUserAgent string) (*model.ChannelPatch, *model.DiagnosticSession) {
	t.Helper()
	ctx := context.Background()
	channel, err := op.ChannelGet(channelID, ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := channel.Keys[0]
	endpoint := channel.BaseUrls[0].URL
	now := time.Now().UTC()
	session := &model.DiagnosticSession{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: "model-a", WireProtocol: channel.Type,
		Endpoint: endpoint, EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint),
		ScopeFingerprint: model.CapabilityScopeFingerprint(channel, key, endpoint), ConfigVersion: channel.ConfigVersion,
		Mode: model.DiagnosticModeLive, Trigger: model.DiagnosticTriggerManual, Status: model.DiagnosticSessionQueued,
		RootCause: model.RootCauseProtocolDrift, MaxAttempts: 2, Deadline: now.Add(5 * time.Minute),
	}
	if err := op.DiagnosticSessionCreate(ctx, session); err != nil {
		t.Fatal(err)
	}
	before := model.NewChannelPatchSnapshot(channel)
	after := before
	after.UserAgent = nextUserAgent
	beforeJSON, _ := json.Marshal(before.UserAgent)
	afterJSON, _ := json.Marshal(after.UserAgent)
	patch := &model.ChannelPatch{
		ChannelID: channel.ID, DiagnosticSessionID: session.ID,
		ExpectedScopeFingerprint: session.ScopeFingerprint, BaseChannelVersion: channel.ConfigVersion,
		Confidence:     model.PatchConfidenceMedium,
		Changes:        []model.ChannelPatchChange{{Field: "user_agent", Before: beforeJSON, After: afterJSON, EvidenceVariantIDs: []string{"ua"}}},
		BeforeSnapshot: before, AfterSnapshot: after, VerificationModel: session.Model,
		VerificationEndpointFingerprint: session.EndpointFingerprint, MaxLiveRequests: 1, Status: model.ChannelPatchPreviewed,
	}
	if err := op.ChannelPatchCreate(ctx, patch); err != nil {
		t.Fatal(err)
	}
	return patch, session
}

func TestPatchServiceAppliesWithOptimisticVersionAndVerifiesOnce(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	patch, _ := createApplyPatch(t, channel.ID, "codex-tui/1")
	fake := &fakeDiagnosticExecutor{succeedOn: DimensionBaseline}
	service := NewPatchService(testSelfHealingConfig(), fake)
	routingBefore := routingstate.Current()
	result, err := service.Apply(context.Background(), patch.ID)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if result.Status != model.ChannelPatchApplied || result.VerificationRootCause != model.RootCauseNone {
		t.Fatalf("applied patch = %#v", result)
	}
	updated, err := op.ChannelGet(channel.ID, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.UserAgent != "codex-tui/1" || updated.ConfigVersion != channel.ConfigVersion+1 {
		t.Fatalf("updated channel = %#v", updated)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 || fake.calls[0].Dimension != DimensionBaseline {
		t.Fatalf("verification calls = %#v, want exactly one baseline", fake.calls)
	}
	select {
	case <-routingBefore.Changed:
	default:
		t.Fatal("self-healing apply did not publish a routing change")
	}
}

func TestPatchServiceRollsBackAfterVerificationFailure(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	channel.UserAgent = "old-agent"
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, UserAgent: &channel.UserAgent}, context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := op.ChannelGet(channel.ID, context.Background())
	patch, _ := createApplyPatch(t, channel.ID, "bad-agent")
	service := NewPatchService(testSelfHealingConfig(), &fakeDiagnosticExecutor{})
	routingBefore := routingstate.Current()
	result, err := service.Apply(context.Background(), patch.ID)
	if !errors.Is(err, ErrPatchInvalid) || result.Status != model.ChannelPatchRolledBack {
		t.Fatalf("rollback result = %#v, err=%v", result, err)
	}
	rolledBack, getErr := op.ChannelGet(channel.ID, context.Background())
	if getErr != nil {
		t.Fatal(getErr)
	}
	if rolledBack.UserAgent != "old-agent" || rolledBack.ConfigVersion != current.ConfigVersion+2 {
		t.Fatalf("rolled back channel = %#v", rolledBack)
	}
	select {
	case <-routingBefore.Changed:
	default:
		t.Fatal("self-healing apply/rollback did not publish a routing change")
	}
}

func TestPatchServiceRejectsStaleVersionWithoutOverwritingManualEdit(t *testing.T) {
	channel := initSelfHealingWorkerDB(t)
	patch, _ := createApplyPatch(t, channel.ID, "diagnosed-agent")
	manual := "manual-agent"
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, UserAgent: &manual}, context.Background()); err != nil {
		t.Fatal(err)
	}
	service := NewPatchService(testSelfHealingConfig(), &fakeDiagnosticExecutor{succeedOn: DimensionBaseline})
	_, err := service.Apply(context.Background(), patch.ID)
	if !errors.Is(err, ErrPatchConflict) {
		t.Fatalf("stale apply error = %v, want patch conflict", err)
	}
	current, _ := op.ChannelGet(channel.ID, context.Background())
	if current.UserAgent != manual {
		t.Fatalf("stale patch overwrote manual edit: %#v", current)
	}
	loaded, _ := op.ChannelPatchGet(context.Background(), patch.ID)
	if loaded.Status != model.ChannelPatchRejected {
		t.Fatalf("stale patch status = %s, want rejected", loaded.Status)
	}
}
