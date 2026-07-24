package selfheal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/channelstate"
	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

var (
	ErrPatchConflict = errors.New("channel patch scope/version conflict")
	ErrPatchInvalid  = errors.New("channel patch is not safe to apply")
)

type PatchService struct {
	config   conf.SelfHealing
	executor DiagnosticExecutor
}

func NewPatchService(config conf.SelfHealing, executor DiagnosticExecutor) *PatchService {
	if executor == nil {
		executor = IsolatedExecutor{}
	}
	return &PatchService{config: config, executor: executor}
}

func (s *PatchService) Apply(ctx context.Context, patchID string) (*model.ChannelPatch, error) {
	if !s.config.Enabled {
		return nil, ErrSelfHealingDisabled
	}
	patch, err := op.ChannelPatchGet(ctx, patchID)
	if err != nil {
		return nil, err
	}
	if patch.Status != model.ChannelPatchPreviewed {
		return patch, fmt.Errorf("%w: patch status is %s", op.ErrConflict, patch.Status)
	}
	session, err := op.DiagnosticSessionGet(ctx, patch.DiagnosticSessionID)
	if err != nil {
		return patch, err
	}
	channel, err := op.ChannelGet(patch.ChannelID, ctx)
	if err != nil {
		return patch, err
	}
	if !channel.SelfHealingEnabled {
		return patch, ErrChannelSelfHealingDisabled
	}
	key, err := selectDiagnosticKey(channel, session.ChannelKeyID)
	if err != nil {
		return patch, err
	}
	if err := validatePatchScope(patch, session, channel, key); err != nil {
		_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchPreviewed}, model.ChannelPatchRejected, err.Error())
		return patch, err
	}
	if err := op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchPreviewed}, model.ChannelPatchApplying, ""); err != nil {
		return patch, err
	}
	request, err := patchUpdateRequest(patch.ChannelID, patch.AfterSnapshot)
	if err != nil {
		_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRejected, err.Error())
		return patch, err
	}
	updated, err := op.ChannelUpdateExpectedVersion(request, patch.BaseChannelVersion, ctx)
	if err != nil {
		_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRejected, err.Error())
		if errors.Is(err, op.ErrConflict) {
			return patch, fmt.Errorf("%w: %v", ErrPatchConflict, err)
		}
		return patch, err
	}
	channelstate.Invalidate(channel.ID, channel, updated)

	verification := s.verify(ctx, updated, key, session)
	if err := op.ChannelPatchRecordVerification(ctx, patch.ID, op.DiagnosticPatchVerification{
		HTTPStatus: verification.HTTPStatus, ErrorLevel: verification.Classification.Classification.Level.String(),
		RootCause: verification.Classification.RootCause, Reason: verification.Classification.Classification.Reason,
		Fingerprint: verification.ResponseFingerprint,
	}); err != nil {
		// A patch must never remain in the applying state merely because the
		// evidence write failed. The configuration has already changed, so use
		// the same version-checked compensation path as a failed verification.
		rollbackRequest, requestErr := patchUpdateRequest(patch.ChannelID, patch.BeforeSnapshot)
		if requestErr != nil {
			return s.markRollbackFailed(ctx, patch, fmt.Errorf("record verification: %v; build rollback: %w", err, requestErr))
		}
		rolledBack, rollbackErr := op.ChannelUpdateExpectedVersion(rollbackRequest, updated.ConfigVersion, ctx)
		if rollbackErr != nil {
			return s.markRollbackFailed(ctx, patch, fmt.Errorf("record verification: %v; rollback: %w", err, rollbackErr))
		}
		channelstate.Invalidate(updated.ID, updated, rolledBack)
		_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRolledBack, err.Error())
		loaded, _ := op.ChannelPatchGet(ctx, patch.ID)
		return loaded, fmt.Errorf("%w: verification evidence could not be stored; configuration was rolled back", ErrPatchInvalid)
	}
	if verification.Success {
		if err := PersistPatchVerificationBaseline(ctx, s.config, session, updated, key, verification); err != nil {
			log.Warnf("persist self-healing verification baseline failed: %v", err)
		}
		if err := op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchApplied, ""); err != nil {
			return patch, err
		}
		metrics.RecordSelfHealingPatch("apply", string(model.ChannelPatchApplied), string(patch.Confidence))
		if loaded, loadErr := op.ChannelPatchGet(ctx, patch.ID); loadErr == nil {
			return loaded, nil
		}
		return patch, nil
	}

	rollbackRequest, err := patchUpdateRequest(patch.ChannelID, patch.BeforeSnapshot)
	if err != nil {
		return s.markRollbackFailed(ctx, patch, err)
	}
	rolledBack, rollbackErr := op.ChannelUpdateExpectedVersion(rollbackRequest, updated.ConfigVersion, ctx)
	if rollbackErr != nil {
		return s.markRollbackFailed(ctx, patch, fmt.Errorf("verification failed (%s); rollback failed: %w", verification.Classification.Classification.Reason, rollbackErr))
	}
	channelstate.Invalidate(updated.ID, updated, rolledBack)
	reason := verification.Classification.Classification.Reason
	_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRolledBack, reason)
	metrics.RecordSelfHealingAutoRollback()
	metrics.RecordSelfHealingPatch("apply", string(model.ChannelPatchRolledBack), string(patch.Confidence))
	loaded, _ := op.ChannelPatchGet(ctx, patch.ID)
	return loaded, fmt.Errorf("%w: verification failed and configuration was rolled back", ErrPatchInvalid)
}

// Rollback restores an already applied patch without issuing a provider
// request. It is version checked so a later administrator edit is never
// overwritten by a historical rollback action.
func (s *PatchService) Rollback(ctx context.Context, patchID string) (*model.ChannelPatch, error) {
	patch, err := op.ChannelPatchGet(ctx, patchID)
	if err != nil {
		return nil, err
	}
	if patch.Status != model.ChannelPatchApplied {
		return patch, fmt.Errorf("%w: patch status is %s", op.ErrConflict, patch.Status)
	}
	session, err := op.DiagnosticSessionGet(ctx, patch.DiagnosticSessionID)
	if err != nil {
		return patch, err
	}
	channel, err := op.ChannelGet(patch.ChannelID, ctx)
	if err != nil {
		return patch, err
	}
	if session.ChannelID != channel.ID || session.EndpointFingerprint != patch.VerificationEndpointFingerprint ||
		channel.ConfigVersion != patch.BaseChannelVersion+1 ||
		!snapshotsEqual(model.NewChannelPatchSnapshot(channel), patch.AfterSnapshot) {
		// No rollback has been attempted yet, so keep the patch in its applied
		// state; the operator resolves the drift instead of seeing a phantom
		// rollback failure.
		return patch, fmt.Errorf("%w: channel configuration changed after patch application", ErrPatchConflict)
	}
	if err := op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplied}, model.ChannelPatchApplying, ""); err != nil {
		return patch, err
	}
	request, err := patchUpdateRequest(patch.ChannelID, patch.BeforeSnapshot)
	if err != nil {
		return s.markRollbackFailed(ctx, patch, err)
	}
	rolledBack, err := op.ChannelUpdateExpectedVersion(request, channel.ConfigVersion, ctx)
	if err != nil {
		return s.markRollbackFailed(ctx, patch, err)
	}
	channelstate.Invalidate(channel.ID, channel, rolledBack)
	if err := op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRolledBack, "administrator rollback"); err != nil {
		return patch, err
	}
	metrics.RecordSelfHealingPatch("rollback", string(model.ChannelPatchRolledBack), string(patch.Confidence))
	loaded, _ := op.ChannelPatchGet(ctx, patch.ID)
	return loaded, nil
}

func (s *PatchService) verify(ctx context.Context, channel *model.Channel, key model.ChannelKey, session *model.DiagnosticSession) AttemptResult {
	timeout := time.Duration(s.config.Diagnostic.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.executor.Execute(verifyCtx, channel, key, session.Endpoint, minimalDiagnosticRequest(channel.Type, session.Model), Variant{Dimension: DimensionBaseline})
}

func (s *PatchService) markRollbackFailed(ctx context.Context, patch *model.ChannelPatch, err error) (*model.ChannelPatch, error) {
	_ = op.ChannelPatchSetStatus(ctx, patch.ID, []model.ChannelPatchStatus{model.ChannelPatchApplying}, model.ChannelPatchRollbackFailed, err.Error())
	loaded, _ := op.ChannelPatchGet(ctx, patch.ID)
	return loaded, fmt.Errorf("%w: %v", ErrPatchInvalid, err)
}

func validatePatchScope(patch *model.ChannelPatch, session *model.DiagnosticSession, channel *model.Channel, key model.ChannelKey) error {
	if patch == nil || session == nil || channel == nil || patch.ChannelID != channel.ID || patch.DiagnosticSessionID != session.ID {
		return fmt.Errorf("%w: patch/session/channel mismatch", ErrPatchInvalid)
	}
	if channel.ConfigVersion != patch.BaseChannelVersion || session.ConfigVersion != patch.BaseChannelVersion {
		return fmt.Errorf("%w: channel config version changed", ErrPatchConflict)
	}
	if model.CapabilityScopeFingerprint(channel, key, session.Endpoint) != patch.ExpectedScopeFingerprint {
		return fmt.Errorf("%w: channel scope fingerprint changed", ErrPatchConflict)
	}
	if patch.VerificationModel != session.Model || patch.VerificationEndpointFingerprint != session.EndpointFingerprint {
		return fmt.Errorf("%w: verification scope mismatch", ErrPatchInvalid)
	}
	if err := validatePatchSnapshotForChannel(patch.AfterSnapshot, channel); err != nil {
		return fmt.Errorf("%w: after snapshot: %v", ErrPatchInvalid, err)
	}
	if patch.AfterSnapshot.RawPassthrough != patch.BeforeSnapshot.RawPassthrough {
		return fmt.Errorf("%w: raw_passthrough requires explicit golden-sample approval", ErrPatchInvalid)
	}
	currentSnapshot := model.NewChannelPatchSnapshot(channel)
	if !snapshotsEqual(currentSnapshot, patch.BeforeSnapshot) {
		return fmt.Errorf("%w: current patchable settings differ from patch before snapshot", ErrPatchConflict)
	}
	return nil
}

func patchUpdateRequest(channelID int, snapshot model.ChannelPatchSnapshot) (*model.ChannelUpdateRequest, error) {
	if err := validatePatchSnapshot(snapshot); err != nil {
		return nil, err
	}
	paramOverride := ""
	if snapshot.ParamOverride != nil {
		paramOverride = *snapshot.ParamOverride
	}
	return &model.ChannelUpdateRequest{
		ID: channelID, UserAgent: &snapshot.UserAgent,
		CustomHeader: &snapshot.CustomHeader, HeaderRules: &snapshot.HeaderRules,
		JSONRewriteRules: &snapshot.JSONRewriteRules, ParamOverride: &paramOverride,
		RawPassthrough: &snapshot.RawPassthrough,
	}, nil
}

func snapshotsEqual(left, right model.ChannelPatchSnapshot) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func PersistPatchVerificationBaseline(ctx context.Context, config conf.SelfHealing, session *model.DiagnosticSession, channel *model.Channel, key model.ChannelKey, result AttemptResult) error {
	if session == nil || channel == nil || result.Artifact == nil || !result.Success {
		return nil
	}
	now := time.Now().UTC()
	return op.ChannelBaselineCreate(ctx, &model.ChannelBaseline{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: session.Model, WireProtocol: channel.Type,
		Endpoint: session.Endpoint, EndpointFingerprint: session.EndpointFingerprint, ScopeFingerprint: model.CapabilityScopeFingerprint(channel, key, session.Endpoint),
		RequestShape: *result.Artifact, HTTPStatus: result.HTTPStatus, ContentType: firstHeader(result.ResponseHeaders, "content-type"),
		Source: model.ChannelBaselineSourceManualLive, CapturedAt: now, ExpiresAt: now.Add(time.Duration(config.BaselineTTLSeconds) * time.Second), Version: 1,
	})
}
