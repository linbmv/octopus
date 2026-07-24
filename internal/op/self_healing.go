package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func DiagnosticSessionCreate(ctx context.Context, session *model.DiagnosticSession) error {
	if session == nil {
		return fmt.Errorf("%w: diagnostic session is required", ErrInvalidInput)
	}
	if session.ID == "" {
		session.ID = "diag_" + uuid.NewString()
	}
	if session.Status == "" {
		session.Status = model.DiagnosticSessionQueued
	}
	if session.RootCause == "" {
		session.RootCause = model.RootCauseUnknown
	}
	if session.ConfigVersion <= 0 {
		session.ConfigVersion = 1
	}
	if session.Status == model.DiagnosticSessionQueued || session.Status == model.DiagnosticSessionRunning {
		activeKey := fmt.Sprintf("channel:%d", session.ChannelID)
		session.ActiveKey = &activeKey
	} else {
		session.ActiveKey = nil
	}
	*session = session.Normalize()
	if !session.Valid() {
		return fmt.Errorf("%w: invalid diagnostic session", ErrInvalidInput)
	}
	database := db.GetDB()
	if database == nil {
		return errors.New("database is not initialized")
	}
	if err := database.WithContext(ctx).Create(session).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintError(err) {
			return fmt.Errorf("%w: an active diagnostic session already exists for channel %d", ErrConflict, session.ChannelID)
		}
		return fmt.Errorf("create diagnostic session: %w", err)
	}
	return nil
}

func DiagnosticSessionGet(ctx context.Context, sessionID string) (*model.DiagnosticSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("%w: diagnostic session id is required", ErrInvalidInput)
	}
	var session model.DiagnosticSession
	err := db.GetDB().WithContext(ctx).First(&session, "id = ?", sessionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func DiagnosticSessionList(ctx context.Context, channelID, limit int) ([]model.DiagnosticSession, error) {
	if channelID <= 0 {
		return nil, fmt.Errorf("%w: channel id must be positive", ErrInvalidInput)
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var sessions []model.DiagnosticSession
	err := db.GetDB().WithContext(ctx).Where("channel_id = ?", channelID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&sessions).Error
	return sessions, err
}

func DiagnosticSessionStart(ctx context.Context, sessionID string, startedAt time.Time) error {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	result := db.GetDB().WithContext(ctx).Model(&model.DiagnosticSession{}).
		Where("id = ? AND status = ? AND deadline > ?", sessionID, model.DiagnosticSessionQueued, startedAt).
		Updates(map[string]interface{}{"status": model.DiagnosticSessionRunning, "started_at": startedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: diagnostic session is not queued or has expired", ErrConflict)
	}
	return nil
}

type DiagnosticSessionFinish struct {
	Status      model.DiagnosticSessionStatus
	RootCause   model.RootCause
	ErrorLevel  string
	ErrorReason string
	StopReason  string
	CompletedAt time.Time
}

func DiagnosticSessionFinishUpdate(ctx context.Context, sessionID string, finish DiagnosticSessionFinish) error {
	if finish.Status != model.DiagnosticSessionCompleted && finish.Status != model.DiagnosticSessionFailed &&
		finish.Status != model.DiagnosticSessionCanceled && finish.Status != model.DiagnosticSessionExpired {
		return fmt.Errorf("%w: invalid terminal diagnostic status", ErrInvalidInput)
	}
	if !finish.RootCause.Valid() {
		return fmt.Errorf("%w: invalid diagnostic root cause", ErrInvalidInput)
	}
	if finish.CompletedAt.IsZero() {
		finish.CompletedAt = time.Now().UTC()
	}
	normalized := model.DiagnosticSession{ErrorReason: finish.ErrorReason, StopReason: finish.StopReason}.Normalize()
	result := db.GetDB().WithContext(ctx).Model(&model.DiagnosticSession{}).
		Where("id = ? AND status IN ?", sessionID, []model.DiagnosticSessionStatus{model.DiagnosticSessionQueued, model.DiagnosticSessionRunning}).
		Updates(map[string]interface{}{
			"status": finish.Status, "root_cause": finish.RootCause, "error_level": finish.ErrorLevel,
			"error_reason": normalized.ErrorReason, "stop_reason": normalized.StopReason,
			"completed_at": finish.CompletedAt, "active_key": nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: diagnostic session is already terminal", ErrConflict)
	}
	return nil
}

func DiagnosticAttemptCreate(ctx context.Context, attempt *model.DiagnosticAttempt) error {
	if attempt == nil {
		return fmt.Errorf("%w: diagnostic attempt is required", ErrInvalidInput)
	}
	attempt.Normalize()
	if attempt.Status == "" {
		attempt.Status = model.DiagnosticAttemptRunning
	}
	if attempt.RootCause == "" {
		attempt.RootCause = model.RootCauseUnknown
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}
	if !attempt.Valid() || (attempt.Status != model.DiagnosticAttemptPending && attempt.Status != model.DiagnosticAttemptRunning) {
		return fmt.Errorf("%w: invalid diagnostic attempt", ErrInvalidInput)
	}
	database := db.GetDB().WithContext(ctx)
	var session model.DiagnosticSession
	if err := database.Select("id", "status", "attempt_count", "max_attempts", "deadline").First(&session, "id = ?", attempt.SessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if session.Status != model.DiagnosticSessionRunning || session.Deadline.Before(time.Now().UTC()) || session.AttemptCount >= session.MaxAttempts {
		return fmt.Errorf("%w: diagnostic session cannot accept another attempt", ErrConflict)
	}
	if err := database.Create(attempt).Error; err != nil {
		return fmt.Errorf("create diagnostic attempt: %w", err)
	}
	return nil
}

func DiagnosticAttemptSetRequestShape(ctx context.Context, attemptID uint, artifact *requestartifact.Artifact) error {
	if attemptID == 0 || artifact == nil || artifact.ShapeSHA256 == "" {
		return fmt.Errorf("%w: invalid diagnostic request shape", ErrInvalidInput)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("encode diagnostic request shape: %w", err)
	}
	result := db.GetDB().WithContext(ctx).Model(&model.DiagnosticAttempt{}).
		Where("id = ? AND status IN ?", attemptID, []model.DiagnosticAttemptStatus{model.DiagnosticAttemptPending, model.DiagnosticAttemptRunning}).
		UpdateColumn("request_shape", string(encoded))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: diagnostic attempt is already terminal", ErrConflict)
	}
	return nil
}

type DiagnosticAttemptFinish struct {
	Status              model.DiagnosticAttemptStatus
	ResponseHeaders     map[string][]string
	HTTPStatus          int
	ErrorLevel          string
	RootCause           model.RootCause
	ErrorReason         string
	ShapeDiff           []string
	ResponseFingerprint string
	Success             bool
	DurationMS          int64
	CostUSD             float64
	FinishedAt          time.Time
}

func DiagnosticAttemptFinishUpdate(ctx context.Context, attemptID uint, finish DiagnosticAttemptFinish) error {
	if attemptID == 0 || (finish.Status != model.DiagnosticAttemptSuccess && finish.Status != model.DiagnosticAttemptFailed && finish.Status != model.DiagnosticAttemptSkipped) ||
		!finish.RootCause.Valid() || finish.DurationMS < 0 || finish.CostUSD < 0 {
		return fmt.Errorf("%w: invalid diagnostic attempt result", ErrInvalidInput)
	}
	if finish.FinishedAt.IsZero() {
		finish.FinishedAt = time.Now().UTC()
	}
	clean := model.DiagnosticAttempt{
		ResponseHeaders: finish.ResponseHeaders, ErrorReason: finish.ErrorReason, ShapeDiff: finish.ShapeDiff,
		ResponseFingerprint: finish.ResponseFingerprint,
	}
	clean.Normalize()
	encodedHeaders, err := json.Marshal(clean.ResponseHeaders)
	if err != nil {
		return fmt.Errorf("encode diagnostic response headers: %w", err)
	}
	encodedShapeDiff, err := json.Marshal(clean.ShapeDiff)
	if err != nil {
		return fmt.Errorf("encode diagnostic shape diff: %w", err)
	}
	database := db.GetDB().WithContext(ctx)
	return database.Transaction(func(tx *gorm.DB) error {
		var attempt model.DiagnosticAttempt
		if err := tx.Select("id", "session_id", "status").First(&attempt, attemptID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if attempt.Status != model.DiagnosticAttemptPending && attempt.Status != model.DiagnosticAttemptRunning {
			return fmt.Errorf("%w: diagnostic attempt is already terminal", ErrConflict)
		}
		updates := map[string]interface{}{
			"status": finish.Status, "response_headers": string(encodedHeaders), "http_status": finish.HTTPStatus,
			"error_level": finish.ErrorLevel, "root_cause": finish.RootCause, "error_reason": clean.ErrorReason,
			"shape_diff": string(encodedShapeDiff), "response_fingerprint": clean.ResponseFingerprint,
			"success": finish.Success, "duration_ms": finish.DurationMS, "cost_usd": finish.CostUSD, "finished_at": finish.FinishedAt,
		}
		if result := tx.Model(&model.DiagnosticAttempt{}).Where("id = ? AND status IN ?", attemptID,
			[]model.DiagnosticAttemptStatus{model.DiagnosticAttemptPending, model.DiagnosticAttemptRunning}).Updates(updates); result.Error != nil {
			return result.Error
		} else if result.RowsAffected != 1 {
			return fmt.Errorf("%w: diagnostic attempt changed concurrently", ErrConflict)
		}
		result := tx.Model(&model.DiagnosticSession{}).
			Where("id = ? AND status = ? AND attempt_count < max_attempts", attempt.SessionID, model.DiagnosticSessionRunning).
			Updates(map[string]interface{}{
				"attempt_count":  gorm.Expr("attempt_count + ?", 1),
				"spent_cost_usd": gorm.Expr("spent_cost_usd + ?", finish.CostUSD),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: diagnostic session attempt budget changed concurrently", ErrConflict)
		}
		return nil
	})
}

func DiagnosticAttemptList(ctx context.Context, sessionID string) ([]model.DiagnosticAttempt, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("%w: diagnostic session id is required", ErrInvalidInput)
	}
	var attempts []model.DiagnosticAttempt
	if err := db.GetDB().WithContext(ctx).Where("session_id = ?", sessionID).
		Order("started_at ASC, id ASC").Find(&attempts).Error; err != nil {
		return nil, err
	}
	return attempts, nil
}

func ChannelPatchCreate(ctx context.Context, patch *model.ChannelPatch) error {
	if patch == nil {
		return fmt.Errorf("%w: channel patch is required", ErrInvalidInput)
	}
	if patch.ID == "" {
		patch.ID = "patch_" + uuid.NewString()
	}
	if patch.Status == "" {
		patch.Status = model.ChannelPatchPreviewed
	}
	patch.Normalize()
	if !patch.Valid() {
		return fmt.Errorf("%w: invalid channel patch", ErrInvalidInput)
	}
	if err := db.GetDB().WithContext(ctx).Create(patch).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintError(err) {
			return fmt.Errorf("%w: duplicate channel patch", ErrConflict)
		}
		return err
	}
	return nil
}

func ChannelPatchGet(ctx context.Context, patchID string) (*model.ChannelPatch, error) {
	var patch model.ChannelPatch
	err := db.GetDB().WithContext(ctx).First(&patch, "id = ?", patchID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &patch, nil
}

func ChannelPatchListBySession(ctx context.Context, sessionID string) ([]model.ChannelPatch, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("%w: diagnostic session id is required", ErrInvalidInput)
	}
	var patches []model.ChannelPatch
	err := db.GetDB().WithContext(ctx).Where("diagnostic_session_id = ?", sessionID).Order("created_at ASC, id ASC").Find(&patches).Error
	return patches, err
}

// ChannelPatchListByChannel returns recent patch metadata for the channel.
// Callers must apply an explicit limit because patch evidence is retained for
// audit purposes and can outlive the diagnostic session UI.
func ChannelPatchListByChannel(ctx context.Context, channelID, limit int) ([]model.ChannelPatch, error) {
	if channelID <= 0 {
		return nil, fmt.Errorf("%w: channel id must be positive", ErrInvalidInput)
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var patches []model.ChannelPatch
	err := db.GetDB().WithContext(ctx).Where("channel_id = ?", channelID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&patches).Error
	return patches, err
}

func ChannelPatchSetStatus(ctx context.Context, patchID string, from []model.ChannelPatchStatus, to model.ChannelPatchStatus, applyError string) error {
	if patchID == "" || len(from) == 0 || !to.Valid() {
		return fmt.Errorf("%w: invalid channel patch transition", ErrInvalidInput)
	}
	normalized := model.ChannelPatch{ApplyError: applyError}
	normalized.Normalize()
	result := db.GetDB().WithContext(ctx).Model(&model.ChannelPatch{}).
		Where("id = ? AND status IN ?", patchID, from).
		Updates(map[string]interface{}{"status": to, "apply_error": normalized.ApplyError})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: channel patch state changed concurrently", ErrConflict)
	}
	return nil
}

func ChannelPatchRecordVerification(ctx context.Context, patchID string, result DiagnosticPatchVerification) error {
	if strings.TrimSpace(patchID) == "" || !result.RootCause.Valid() {
		return fmt.Errorf("%w: invalid patch verification evidence", ErrInvalidInput)
	}
	if result.VerifiedAt.IsZero() {
		result.VerifiedAt = time.Now().UTC()
	}
	normalized := model.DiagnosticSession{ErrorReason: result.Reason}.Normalize()
	updates := map[string]interface{}{
		"verification_http_status": result.HTTPStatus,
		"verification_error_level": result.ErrorLevel,
		"verification_root_cause":  result.RootCause,
		"verification_reason":      normalized.ErrorReason,
		"verification_fingerprint": strings.TrimSpace(result.Fingerprint),
		"verified_at":              result.VerifiedAt,
	}
	resultDB := db.GetDB().WithContext(ctx).Model(&model.ChannelPatch{}).
		Where("id = ? AND status = ?", patchID, model.ChannelPatchApplying).Updates(updates)
	if resultDB.Error != nil {
		return resultDB.Error
	}
	if resultDB.RowsAffected != 1 {
		return fmt.Errorf("%w: channel patch is not applying", ErrConflict)
	}
	return nil
}

type DiagnosticPatchVerification struct {
	HTTPStatus  int
	ErrorLevel  string
	RootCause   model.RootCause
	Reason      string
	Fingerprint string
	VerifiedAt  time.Time
}

func SelfHealingCleanup(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	database := db.GetDB().WithContext(ctx)
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.DiagnosticSession{}).
			Where("status IN ? AND deadline <= ?", []model.DiagnosticSessionStatus{model.DiagnosticSessionQueued, model.DiagnosticSessionRunning}, now).
			Updates(map[string]interface{}{"status": model.DiagnosticSessionExpired, "completed_at": now, "active_key": nil, "stop_reason": "diagnostic session expired"}).Error; err != nil {
			return err
		}
		cutoff := now.Add(-24 * time.Hour)
		var staleIDs []string
		if err := tx.Model(&model.DiagnosticSession{}).Where("status IN ? AND completed_at < ?",
			[]model.DiagnosticSessionStatus{model.DiagnosticSessionCompleted, model.DiagnosticSessionFailed, model.DiagnosticSessionCanceled, model.DiagnosticSessionExpired}, cutoff).
			Pluck("id", &staleIDs).Error; err != nil {
			return err
		}
		if len(staleIDs) == 0 {
			return nil
		}
		if err := tx.Where("session_id IN ?", staleIDs).Delete(&model.DiagnosticAttempt{}).Error; err != nil {
			return err
		}
		if err := tx.Where("diagnostic_session_id IN ?", staleIDs).Delete(&model.ChannelPatch{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", staleIDs).Delete(&model.DiagnosticSession{}).Error
	})
}

func deleteSelfHealingChannelTx(tx *gorm.DB, channelID int) error {
	if tx == nil || channelID <= 0 || !tx.Migrator().HasTable(&model.DiagnosticSession{}) {
		return nil
	}
	var sessionIDs []string
	if err := tx.Model(&model.DiagnosticSession{}).Where("channel_id = ?", channelID).Pluck("id", &sessionIDs).Error; err != nil {
		return err
	}
	if len(sessionIDs) > 0 {
		if err := tx.Where("session_id IN ?", sessionIDs).Delete(&model.DiagnosticAttempt{}).Error; err != nil {
			return err
		}
		if err := tx.Where("diagnostic_session_id IN ?", sessionIDs).Delete(&model.ChannelPatch{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("channel_id = ?", channelID).Delete(&model.DiagnosticSession{}).Error
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate entry") || strings.Contains(message, "duplicate key")
}
