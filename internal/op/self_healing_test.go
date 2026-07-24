package op

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm/httpclient"
)

func newDiagnosticSession(now time.Time, channelID int) *model.DiagnosticSession {
	return &model.DiagnosticSession{
		ChannelID: channelID, ChannelKeyID: 2, Model: "model-a", WireProtocol: "openai/responses",
		Endpoint: "https://provider.test", EndpointFingerprint: model.CapabilityEndpointFingerprint("https://provider.test"),
		ScopeFingerprint: "scope", ConfigVersion: 1, Mode: model.DiagnosticModeLive,
		Trigger: model.DiagnosticTriggerManual, Status: model.DiagnosticSessionQueued, RootCause: model.RootCauseUnknown,
		MaxAttempts: 2, ReservedCostUSD: 0.002, Deadline: now.Add(5 * time.Minute),
	}
}

func TestDiagnosticSessionEnforcesOneActiveSessionPerChannel(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first := newDiagnosticSession(now, 11)
	if err := DiagnosticSessionCreate(ctx, first); err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second := newDiagnosticSession(now, 11)
	if err := DiagnosticSessionCreate(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("create concurrent session error = %v, want conflict", err)
	}
	if err := DiagnosticSessionFinishUpdate(ctx, first.ID, DiagnosticSessionFinish{
		Status: model.DiagnosticSessionCompleted, RootCause: model.RootCauseNone, CompletedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("finish first session: %v", err)
	}
	if err := DiagnosticSessionCreate(ctx, second); err != nil {
		t.Fatalf("create session after completion: %v", err)
	}
}

func TestDiagnosticAttemptLifecycleUpdatesSessionBudget(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	session := newDiagnosticSession(now, 12)
	if err := DiagnosticSessionCreate(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := DiagnosticSessionStart(ctx, session.ID, now); err != nil {
		t.Fatal(err)
	}
	artifact := requestartifact.Build(&httpclient.Request{
		Method: http.MethodPost, URL: "https://provider.test/v1/responses", Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{"model":"model-a","input":"probe"}`),
	}, "openai/responses", "model-a", requestartifact.RewriteSummary{})
	attempt := &model.DiagnosticAttempt{SessionID: session.ID, VariantID: "baseline", Status: model.DiagnosticAttemptRunning,
		RequestShape: *artifact, RootCause: model.RootCauseUnknown, StartedAt: now}
	if err := DiagnosticAttemptCreate(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if err := DiagnosticAttemptFinishUpdate(ctx, attempt.ID, DiagnosticAttemptFinish{
		Status: model.DiagnosticAttemptFailed, HTTPStatus: 400, ErrorLevel: "client", RootCause: model.RootCauseProtocolDrift,
		ErrorReason: "unknown field", ShapeDiff: []string{"body:/input"}, DurationMS: 15, CostUSD: 0.001, FinishedAt: now.Add(15 * time.Millisecond),
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	loaded, err := DiagnosticSessionGet(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AttemptCount != 1 || loaded.SpentCostUSD != 0.001 {
		t.Fatalf("session usage = attempts:%d cost:%f", loaded.AttemptCount, loaded.SpentCostUSD)
	}
	attempts, err := DiagnosticAttemptList(ctx, session.ID)
	if err != nil || len(attempts) != 1 || attempts[0].RootCause != model.RootCauseProtocolDrift {
		t.Fatalf("attempts = %#v, err=%v", attempts, err)
	}
}

func TestChannelPatchRoundTripContainsOnlySafeSnapshot(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	session := newDiagnosticSession(now, 13)
	if err := DiagnosticSessionCreate(ctx, session); err != nil {
		t.Fatal(err)
	}
	before := model.ChannelPatchSnapshot{UserAgent: "old"}
	after := model.ChannelPatchSnapshot{UserAgent: "codex-cli"}
	patch := &model.ChannelPatch{
		ChannelID: 13, DiagnosticSessionID: session.ID, ExpectedScopeFingerprint: "scope", BaseChannelVersion: 1,
		Confidence: model.PatchConfidenceHigh, Changes: []model.ChannelPatchChange{{
			Field: "user_agent", Before: json.RawMessage(`"old"`), After: json.RawMessage(`"codex-cli"`), EvidenceVariantIDs: []string{"ua-codex"},
		}}, BeforeSnapshot: before, AfterSnapshot: after, VerificationModel: "model-a",
		VerificationEndpointFingerprint: model.CapabilityEndpointFingerprint("https://provider.test"), MaxLiveRequests: 1,
		Status: model.ChannelPatchPreviewed,
	}
	if err := ChannelPatchCreate(ctx, patch); err != nil {
		t.Fatalf("create patch: %v", err)
	}
	loaded, err := ChannelPatchGet(ctx, patch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AfterSnapshot.UserAgent != "codex-cli" || len(loaded.Changes) != 1 {
		t.Fatalf("loaded patch = %#v", loaded)
	}
	if err := ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchPreviewed}, model.ChannelPatchApplying, ""); err != nil {
		t.Fatalf("transition patch: %v", err)
	}
	if err := ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchPreviewed}, model.ChannelPatchApplied, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch transition error = %v, want conflict", err)
	}
}

func TestSelfHealingCleanupExpiresAndPrunesSessions(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	session := newDiagnosticSession(now.Add(-2*time.Hour), 14)
	session.Deadline = now.Add(-time.Hour)
	if err := DiagnosticSessionCreate(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := SelfHealingCleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := DiagnosticSessionGet(ctx, session.ID)
	if err != nil || loaded.Status != model.DiagnosticSessionExpired || loaded.ActiveKey != nil {
		t.Fatalf("expired session = %#v, err=%v", loaded, err)
	}
	if err := SelfHealingCleanup(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := DiagnosticSessionGet(ctx, session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned session error = %v, want not found", err)
	}
}
