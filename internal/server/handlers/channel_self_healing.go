package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/selfheal"
	"github.com/bestruirui/octopus/internal/selfheal/importer"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

var newSelfHealingPatchService = func() *selfheal.PatchService {
	return selfheal.NewPatchService(conf.Current().SelfHealing, nil)
}

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("/:id/self-healing", http.MethodGet).Handle(getChannelSelfHealingStatus)).
		AddRoute(router.NewRoute("/:id/self-healing/diagnostics/:diagnostic_id", http.MethodGet).Handle(getChannelSelfHealingDiagnostic)).
		AddRoute(router.NewRoute("/:id/self-healing/baselines", http.MethodGet).Handle(listChannelSelfHealingBaselines)).
		AddRoute(router.NewRoute("/:id/self-healing/patches", http.MethodGet).Handle(listChannelSelfHealingPatches))

	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/:id/self-healing/preview", http.MethodPost).Handle(previewChannelSelfHealing)).
		AddRoute(router.NewRoute("/:id/self-healing/diagnostics", http.MethodPost).Handle(createChannelSelfHealingDiagnostic)).
		AddRoute(router.NewRoute("/:id/self-healing/diagnostics/:diagnostic_id/apply", http.MethodPost).Handle(applyChannelSelfHealingPatch)).
		AddRoute(router.NewRoute("/:id/self-healing/rollback/:patch_id", http.MethodPost).Handle(rollbackChannelSelfHealingPatch))
}

type selfHealingSessionResponse struct {
	ID                  string                        `json:"id"`
	ChannelID           int                           `json:"channel_id"`
	Model               string                        `json:"model"`
	WireProtocol        llm.APIFormat                 `json:"wire_protocol"`
	EndpointFingerprint string                        `json:"endpoint_fingerprint"`
	ConfigVersion       int                           `json:"config_version"`
	Mode                model.DiagnosticMode          `json:"mode"`
	Trigger             model.DiagnosticTrigger       `json:"trigger"`
	Status              model.DiagnosticSessionStatus `json:"status"`
	RootCause           model.RootCause               `json:"root_cause"`
	ErrorLevel          string                        `json:"error_level,omitempty"`
	ErrorReason         string                        `json:"error_reason,omitempty"`
	Actor               string                        `json:"actor,omitempty"`
	MaxAttempts         int                           `json:"max_attempts"`
	AttemptCount        int                           `json:"attempt_count"`
	ReservedCostUSD     float64                       `json:"reserved_cost_usd"`
	SpentCostUSD        float64                       `json:"spent_cost_usd"`
	StopReason          string                        `json:"stop_reason,omitempty"`
	Deadline            time.Time                     `json:"deadline"`
	StartedAt           *time.Time                    `json:"started_at,omitempty"`
	CompletedAt         *time.Time                    `json:"completed_at,omitempty"`
	CreatedAt           time.Time                     `json:"created_at"`
	UpdatedAt           time.Time                     `json:"updated_at"`
}

type selfHealingAttemptResponse struct {
	ID               uint                          `json:"id"`
	SessionID        string                        `json:"session_id"`
	VariantID        string                        `json:"variant_id"`
	ParentVariantID  string                        `json:"parent_variant_id,omitempty"`
	ChangedDimension string                        `json:"changed_dimension,omitempty"`
	Status           model.DiagnosticAttemptStatus `json:"status"`
	RequestShape     requestartifact.Artifact      `json:"request_shape"`
	ResponseHeaders  map[string][]string           `json:"response_headers,omitempty"`
	HTTPStatus       int                           `json:"http_status,omitempty"`
	ErrorLevel       string                        `json:"error_level,omitempty"`
	RootCause        model.RootCause               `json:"root_cause"`
	ErrorReason      string                        `json:"error_reason,omitempty"`
	ShapeDiff        []string                      `json:"shape_diff,omitempty"`
	Success          bool                          `json:"success"`
	DurationMS       int64                         `json:"duration_ms,omitempty"`
	CostUSD          float64                       `json:"cost_usd"`
	StartedAt        time.Time                     `json:"started_at"`
	FinishedAt       *time.Time                    `json:"finished_at,omitempty"`
}

type selfHealingPatchChangeResponse struct {
	Field              string   `json:"field"`
	EvidenceVariantIDs []string `json:"evidence_variant_ids,omitempty"`
}

type selfHealingPatchResponse struct {
	ID                     string                           `json:"id"`
	ChannelID              int                              `json:"channel_id"`
	DiagnosticSessionID    string                           `json:"diagnostic_session_id"`
	BaseChannelVersion     int                              `json:"base_channel_version"`
	Confidence             model.PatchConfidence            `json:"confidence"`
	Changes                []selfHealingPatchChangeResponse `json:"changes"`
	MaxLiveRequests        int                              `json:"max_live_requests"`
	Status                 model.ChannelPatchStatus         `json:"status"`
	ApplyError             string                           `json:"apply_error,omitempty"`
	VerificationHTTPStatus int                              `json:"verification_http_status,omitempty"`
	VerificationErrorLevel string                           `json:"verification_error_level,omitempty"`
	VerificationRootCause  model.RootCause                  `json:"verification_root_cause,omitempty"`
	VerificationReason     string                           `json:"verification_reason,omitempty"`
	VerifiedAt             *time.Time                       `json:"verified_at,omitempty"`
	CreatedAt              time.Time                        `json:"created_at"`
	UpdatedAt              time.Time                        `json:"updated_at"`
}

type selfHealingBaselineResponse struct {
	ID                  uint                     `json:"id"`
	ChannelID           int                      `json:"channel_id"`
	Model               string                   `json:"model"`
	WireProtocol        llm.APIFormat            `json:"wire_protocol"`
	EndpointFingerprint string                   `json:"endpoint_fingerprint"`
	RequestShape        requestartifact.Artifact `json:"request_shape"`
	HTTPStatus          int                      `json:"http_status,omitempty"`
	ContentType         string                   `json:"content_type,omitempty"`
	Source              string                   `json:"source"`
	CapturedAt          time.Time                `json:"captured_at"`
	ExpiresAt           time.Time                `json:"expires_at"`
	Version             int                      `json:"version"`
}

func newSelfHealingSessionResponse(session model.DiagnosticSession) selfHealingSessionResponse {
	return selfHealingSessionResponse{
		ID: session.ID, ChannelID: session.ChannelID, Model: session.Model, WireProtocol: session.WireProtocol,
		EndpointFingerprint: session.EndpointFingerprint, ConfigVersion: session.ConfigVersion,
		Mode: session.Mode, Trigger: session.Trigger, Status: session.Status, RootCause: session.RootCause,
		ErrorLevel: session.ErrorLevel, ErrorReason: session.ErrorReason, Actor: session.Actor,
		MaxAttempts: session.MaxAttempts, AttemptCount: session.AttemptCount,
		ReservedCostUSD: session.ReservedCostUSD, SpentCostUSD: session.SpentCostUSD,
		StopReason: session.StopReason, Deadline: session.Deadline, StartedAt: session.StartedAt,
		CompletedAt: session.CompletedAt, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	}
}

func newSelfHealingAttemptResponse(attempt model.DiagnosticAttempt) selfHealingAttemptResponse {
	return selfHealingAttemptResponse{
		ID: attempt.ID, SessionID: attempt.SessionID, VariantID: attempt.VariantID,
		ParentVariantID: attempt.ParentVariantID, ChangedDimension: attempt.ChangedDimension,
		Status: attempt.Status, RequestShape: attempt.RequestShape, ResponseHeaders: attempt.ResponseHeaders,
		HTTPStatus: attempt.HTTPStatus, ErrorLevel: attempt.ErrorLevel, RootCause: attempt.RootCause,
		ErrorReason: attempt.ErrorReason, ShapeDiff: attempt.ShapeDiff, Success: attempt.Success,
		DurationMS: attempt.DurationMS, CostUSD: attempt.CostUSD, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt,
	}
}

func newSelfHealingPatchResponse(patch model.ChannelPatch) selfHealingPatchResponse {
	changes := make([]selfHealingPatchChangeResponse, 0, len(patch.Changes))
	for _, change := range patch.Changes {
		changes = append(changes, selfHealingPatchChangeResponse{Field: change.Field, EvidenceVariantIDs: append([]string(nil), change.EvidenceVariantIDs...)})
	}
	return selfHealingPatchResponse{
		ID: patch.ID, ChannelID: patch.ChannelID, DiagnosticSessionID: patch.DiagnosticSessionID,
		BaseChannelVersion: patch.BaseChannelVersion, Confidence: patch.Confidence, Changes: changes,
		MaxLiveRequests: patch.MaxLiveRequests, Status: patch.Status, ApplyError: patch.ApplyError,
		VerificationHTTPStatus: patch.VerificationHTTPStatus, VerificationErrorLevel: patch.VerificationErrorLevel,
		VerificationRootCause: patch.VerificationRootCause, VerificationReason: patch.VerificationReason,
		VerifiedAt: patch.VerifiedAt, CreatedAt: patch.CreatedAt, UpdatedAt: patch.UpdatedAt,
	}
}

func newSelfHealingBaselineResponse(baseline model.ChannelBaseline) selfHealingBaselineResponse {
	return selfHealingBaselineResponse{
		ID: baseline.ID, ChannelID: baseline.ChannelID, Model: baseline.Model, WireProtocol: baseline.WireProtocol,
		EndpointFingerprint: baseline.EndpointFingerprint, RequestShape: baseline.RequestShape,
		HTTPStatus: baseline.HTTPStatus, ContentType: baseline.ContentType, Source: baseline.Source,
		CapturedAt: baseline.CapturedAt, ExpiresAt: baseline.ExpiresAt, Version: baseline.Version,
	}
}

func getChannelSelfHealingStatus(c *gin.Context) {
	channelID, channel, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	sessions, err := op.DiagnosticSessionList(c.Request.Context(), channelID, selfHealingLimit(c))
	if err != nil {
		respondInternalError(c, "list self-healing diagnostic sessions failed", err)
		return
	}
	patches, err := op.ChannelPatchListByChannel(c.Request.Context(), channelID, selfHealingLimit(c))
	if err != nil {
		respondInternalError(c, "list self-healing channel patches failed", err)
		return
	}
	baselines, err := op.ChannelBaselineList(c.Request.Context(), channelID, selfHealingLimit(c))
	if err != nil {
		respondInternalError(c, "list self-healing channel baselines failed", err)
		return
	}
	responseSessions := make([]selfHealingSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		responseSessions = append(responseSessions, newSelfHealingSessionResponse(session))
	}
	responsePatches := make([]selfHealingPatchResponse, 0, len(patches))
	for _, patch := range patches {
		responsePatches = append(responsePatches, newSelfHealingPatchResponse(patch))
	}
	responseBaselines := make([]selfHealingBaselineResponse, 0, len(baselines))
	for _, baseline := range baselines {
		responseBaselines = append(responseBaselines, newSelfHealingBaselineResponse(baseline))
	}
	worker := selfheal.DefaultWorker()
	var queue any
	if worker != nil {
		queue = worker.Stats()
	}
	config := conf.Current().SelfHealing
	resp.Success(c, map[string]any{
		"global_enabled": config.Enabled, "capture_success_baselines": config.CaptureSuccessBaselines,
		"channel_enabled": channel.SelfHealingEnabled, "channel_config_version": channel.ConfigVersion,
		"worker_available": worker != nil, "queue": queue,
		"sessions": responseSessions, "patches": responsePatches, "baselines": responseBaselines,
	})
}

// runSelfHealingPreview executes a preview and writes the audit event. It
// returns nil after responding with an error so both the standalone preview
// route and the diagnostics mode=preview branch stay behaviorally identical.
func runSelfHealingPreview(c *gin.Context, worker *selfheal.Worker, request selfheal.PreviewRequest) *selfheal.PreviewResult {
	result, err := worker.Preview(c.Request.Context(), request)
	if err != nil {
		respondSelfHealingError(c, err)
		return nil
	}
	middleware.AuditLog(c, middleware.EventChannelSelfHealingPreview, map[string]interface{}{
		"channel_id": request.ChannelID, "root_cause": request.RootCause, "variant_count": len(result.Plan.Variants),
	})
	return result
}

func previewChannelSelfHealing(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	var request struct {
		ChannelKeyID int             `json:"channel_key_id,omitempty"`
		Endpoint     string          `json:"endpoint,omitempty"`
		Model        string          `json:"model,omitempty"`
		RootCause    model.RootCause `json:"root_cause"`
		MaxVariants  int             `json:"max_variants,omitempty"`
	}
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	worker := selfheal.DefaultWorker()
	if worker == nil {
		selfHealingUnavailable(c)
		return
	}
	result := runSelfHealingPreview(c, worker, selfheal.PreviewRequest{
		ChannelID: channelID, ChannelKeyID: request.ChannelKeyID, Endpoint: request.Endpoint,
		Model: request.Model, RootCause: request.RootCause, MaxVariants: request.MaxVariants,
	})
	if result == nil {
		return
	}
	resp.Success(c, result)
}

func createChannelSelfHealingDiagnostic(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	var request struct {
		Mode         model.DiagnosticMode `json:"mode,omitempty"`
		ChannelKeyID int                  `json:"channel_key_id,omitempty"`
		Endpoint     string               `json:"endpoint,omitempty"`
		Model        string               `json:"model,omitempty"`
		RootCause    model.RootCause      `json:"root_cause"`
		MaxCostUSD   float64              `json:"max_cost_usd,omitempty"`
		MaxVariants  int                  `json:"max_variants,omitempty"`
		GoldenSample json.RawMessage      `json:"golden_sample,omitempty"`
	}
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if request.Mode == "" {
		request.Mode = model.DiagnosticModeLive
	}
	worker := selfheal.DefaultWorker()
	if worker == nil {
		selfHealingUnavailable(c)
		return
	}
	if request.Mode == model.DiagnosticModePreview {
		result := runSelfHealingPreview(c, worker, selfheal.PreviewRequest{
			ChannelID: channelID, ChannelKeyID: request.ChannelKeyID, Endpoint: request.Endpoint,
			Model: request.Model, RootCause: request.RootCause, MaxVariants: request.MaxVariants,
		})
		if result == nil {
			return
		}
		resp.Success(c, map[string]any{"mode": model.DiagnosticModePreview, "preview": result})
		return
	}
	if request.Mode == model.DiagnosticModeCompare {
		if len(request.GoldenSample) == 0 {
			resp.ErrorWithCode(c, http.StatusBadRequest, "SELF_HEALING_SAMPLE_REQUIRED", "compare diagnostics require a golden sample")
			return
		}
		result, err := worker.Compare(c.Request.Context(), selfheal.CompareRequest{
			ChannelID: channelID, ChannelKeyID: request.ChannelKeyID, Endpoint: request.Endpoint,
			Model: request.Model, GoldenSample: request.GoldenSample,
		})
		if err != nil {
			respondSelfHealingError(c, err)
			return
		}
		compareActor, _ := c.Get("username")
		middleware.AuditLog(c, middleware.EventChannelSelfHealingCompare, map[string]interface{}{
			"channel_id": channelID, "header_diff_count": len(result.HeaderDiff),
			"body_diff_count": len(result.BodyDiff), "suggested_count": len(result.Suggested),
			"actor": strings.TrimSpace(stringValue(compareActor)),
		})
		resp.Success(c, map[string]any{"mode": model.DiagnosticModeCompare, "compare": result})
		return
	}
	if request.Mode != model.DiagnosticModeLive {
		resp.ErrorWithCode(c, http.StatusBadRequest, "SELF_HEALING_MODE_UNSUPPORTED", "unsupported diagnostic mode")
		return
	}
	actor, _ := c.Get("username")
	report, err := worker.Submit(c.Request.Context(), selfheal.SubmitRequest{
		ChannelID: channelID, ChannelKeyID: request.ChannelKeyID, Endpoint: request.Endpoint,
		Model: request.Model, RootCause: request.RootCause, Trigger: model.DiagnosticTriggerManual,
		Actor: strings.TrimSpace(stringValue(actor)), MaxCostUSD: request.MaxCostUSD,
	})
	if err != nil {
		respondSelfHealingError(c, err)
		return
	}
	middleware.AuditLog(c, middleware.EventChannelSelfHealingDiagnostic, map[string]interface{}{
		"channel_id": channelID, "diagnostic_id": report.Session.ID, "status": report.Session.Status,
		"root_cause": report.Session.RootCause, "accepted": report.Accepted,
	})
	resp.Success(c, map[string]any{
		"session": newSelfHealingSessionResponse(*report.Session), "accepted": report.Accepted,
		"early_stop": report.EarlyStop, "reserved_cost_usd": report.ReservedCostUSD, "queue": report.Queue,
	})
}

func getChannelSelfHealingDiagnostic(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	session, err := op.DiagnosticSessionGet(c.Request.Context(), strings.TrimSpace(c.Param("diagnostic_id")))
	if err != nil {
		respondSelfHealingError(c, err)
		return
	}
	if session.ChannelID != channelID {
		selfHealingNotFound(c)
		return
	}
	attempts, err := op.DiagnosticAttemptList(c.Request.Context(), session.ID)
	if err != nil {
		respondInternalError(c, "list self-healing diagnostic attempts failed", err)
		return
	}
	patches, err := op.ChannelPatchListBySession(c.Request.Context(), session.ID)
	if err != nil {
		respondInternalError(c, "list self-healing diagnostic patches failed", err)
		return
	}
	responseAttempts := make([]selfHealingAttemptResponse, 0, len(attempts))
	for _, attempt := range attempts {
		responseAttempts = append(responseAttempts, newSelfHealingAttemptResponse(attempt))
	}
	responsePatches := make([]selfHealingPatchResponse, 0, len(patches))
	for _, patch := range patches {
		responsePatches = append(responsePatches, newSelfHealingPatchResponse(patch))
	}
	middleware.AuditLog(c, middleware.EventChannelSelfHealingView, map[string]interface{}{
		"channel_id": channelID, "diagnostic_id": session.ID, "status": session.Status, "root_cause": session.RootCause,
	})
	resp.Success(c, map[string]any{
		"session": newSelfHealingSessionResponse(*session), "attempts": responseAttempts, "patches": responsePatches,
	})
}

func listChannelSelfHealingBaselines(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	baselines, err := op.ChannelBaselineList(c.Request.Context(), channelID, selfHealingLimit(c))
	if err != nil {
		respondInternalError(c, "list self-healing channel baselines failed", err)
		return
	}
	result := make([]selfHealingBaselineResponse, 0, len(baselines))
	for _, baseline := range baselines {
		result = append(result, newSelfHealingBaselineResponse(baseline))
	}
	resp.Success(c, result)
}

func listChannelSelfHealingPatches(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	patches, err := op.ChannelPatchListByChannel(c.Request.Context(), channelID, selfHealingLimit(c))
	if err != nil {
		respondInternalError(c, "list self-healing channel patches failed", err)
		return
	}
	result := make([]selfHealingPatchResponse, 0, len(patches))
	for _, patch := range patches {
		result = append(result, newSelfHealingPatchResponse(patch))
	}
	resp.Success(c, result)
}

func applyChannelSelfHealingPatch(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	var request struct {
		PatchID string `json:"patch_id"`
	}
	if err := bindStrictJSON(c, &request); err != nil || strings.TrimSpace(request.PatchID) == "" {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	patch, session, ok := selfHealingPatchScope(c, channelID, request.PatchID)
	if !ok {
		return
	}
	// selfHealingPatchScope loads the session from the patch itself, so only the
	// URL's diagnostic id still needs to match.
	if session.ID != strings.TrimSpace(c.Param("diagnostic_id")) {
		selfHealingNotFound(c)
		return
	}
	result, err := newSelfHealingPatchService().Apply(c.Request.Context(), patch.ID)
	if err != nil {
		middleware.AuditLog(c, middleware.EventChannelSelfHealingApply, map[string]interface{}{
			"channel_id": channelID, "diagnostic_id": session.ID, "patch_id": patch.ID, "status": "failed",
		})
		respondSelfHealingError(c, err)
		return
	}
	middleware.AuditLog(c, middleware.EventChannelSelfHealingApply, map[string]interface{}{
		"channel_id": channelID, "diagnostic_id": session.ID, "patch_id": patch.ID, "status": result.Status,
	})
	resp.Success(c, newSelfHealingPatchResponse(*result))
}

func rollbackChannelSelfHealingPatch(c *gin.Context) {
	channelID, _, ok := selfHealingChannel(c)
	if !ok {
		return
	}
	// Require an empty JSON object so the route keeps the same strict JSON and
	// content-type contract as the other mutating channel APIs; bind before any
	// DB lookups like the other handlers.
	var request struct{}
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	patch, _, ok := selfHealingPatchScope(c, channelID, strings.TrimSpace(c.Param("patch_id")))
	if !ok {
		return
	}
	result, err := newSelfHealingPatchService().Rollback(c.Request.Context(), patch.ID)
	if err != nil {
		middleware.AuditLog(c, middleware.EventChannelSelfHealingRollback, map[string]interface{}{
			"channel_id": channelID, "patch_id": patch.ID, "status": "failed",
		})
		respondSelfHealingError(c, err)
		return
	}
	middleware.AuditLog(c, middleware.EventChannelSelfHealingRollback, map[string]interface{}{
		"channel_id": channelID, "patch_id": patch.ID, "status": result.Status,
	})
	resp.Success(c, newSelfHealingPatchResponse(*result))
}

func selfHealingChannel(c *gin.Context) (int, *model.Channel, bool) {
	channelID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || channelID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return 0, nil, false
	}
	channel, err := op.ChannelGet(channelID, c.Request.Context())
	if err != nil {
		selfHealingNotFound(c)
		return 0, nil, false
	}
	return channelID, channel, true
}

func selfHealingPatchScope(c *gin.Context, channelID int, patchID string) (*model.ChannelPatch, *model.DiagnosticSession, bool) {
	patch, err := op.ChannelPatchGet(c.Request.Context(), strings.TrimSpace(patchID))
	if err != nil {
		respondSelfHealingError(c, err)
		return nil, nil, false
	}
	if patch.ChannelID != channelID {
		selfHealingNotFound(c)
		return nil, nil, false
	}
	session, err := op.DiagnosticSessionGet(c.Request.Context(), patch.DiagnosticSessionID)
	if err != nil {
		respondSelfHealingError(c, err)
		return nil, nil, false
	}
	if session.ChannelID != channelID {
		selfHealingNotFound(c)
		return nil, nil, false
	}
	return patch, session, true
}

func respondSelfHealingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, selfheal.ErrSelfHealingDisabled):
		resp.ErrorWithCode(c, http.StatusConflict, "SELF_HEALING_DISABLED", "self-healing diagnostics are disabled")
	case errors.Is(err, selfheal.ErrChannelSelfHealingDisabled):
		resp.ErrorWithCode(c, http.StatusConflict, "CHANNEL_SELF_HEALING_DISABLED", "self-healing is disabled for this channel")
	case errors.Is(err, selfheal.ErrPatchConflict), errors.Is(err, op.ErrConflict):
		resp.ErrorWithCode(c, http.StatusConflict, "SELF_HEALING_CONFLICT", err.Error())
	case errors.Is(err, selfheal.ErrPatchInvalid):
		resp.ErrorWithCode(c, http.StatusUnprocessableEntity, "SELF_HEALING_PATCH_INVALID", err.Error())
	case errors.Is(err, importer.ErrSampleAuthSecret):
		resp.ErrorWithCode(c, http.StatusUnprocessableEntity, "SELF_HEALING_SAMPLE_SECRET", "golden sample contains authentication secrets; redact them before importing")
	case errors.Is(err, importer.ErrSampleTooLarge), errors.Is(err, importer.ErrSampleInvalidURL),
		errors.Is(err, importer.ErrSampleInvalidJSON), errors.Is(err, importer.ErrSampleEmpty),
		errors.Is(err, importer.ErrSampleUnsupported):
		resp.ErrorWithCode(c, http.StatusUnprocessableEntity, "SELF_HEALING_SAMPLE_INVALID", err.Error())
	case errors.Is(err, op.ErrNotFound):
		selfHealingNotFound(c)
	case errors.Is(err, op.ErrInvalidInput):
		resp.ErrorWithCode(c, http.StatusBadRequest, "SELF_HEALING_INVALID", err.Error())
	default:
		resp.ErrorWithCode(c, http.StatusBadRequest, "SELF_HEALING_INVALID", err.Error())
	}
}

func selfHealingUnavailable(c *gin.Context) {
	resp.ErrorWithCode(c, http.StatusServiceUnavailable, "SELF_HEALING_UNAVAILABLE", "self-healing worker is unavailable")
}

func selfHealingNotFound(c *gin.Context) {
	resp.ErrorWithCode(c, http.StatusNotFound, "SELF_HEALING_NOT_FOUND", "self-healing resource not found")
}

func selfHealingLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || limit <= 0 || limit > 100 {
		return 20
	}
	return limit
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
