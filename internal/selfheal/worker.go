package selfheal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/requestartifact"
	appruntime "github.com/bestruirui/octopus/internal/runtime"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"golang.org/x/time/rate"
)

var (
	ErrSelfHealingDisabled        = errors.New("self-healing diagnostics are disabled")
	ErrChannelSelfHealingDisabled = errors.New("self-healing is disabled for this channel")
	defaultWorker                 atomic.Pointer[Worker]
)

type DiagnosticExecutor interface {
	Execute(context.Context, *model.Channel, model.ChannelKey, string, *llm.Request, Variant) AttemptResult
}

type diagnosticJob struct {
	SessionID string
}

type SubmitRequest struct {
	ChannelID    int                     `json:"channel_id"`
	ChannelKeyID int                     `json:"channel_key_id,omitempty"`
	Endpoint     string                  `json:"endpoint,omitempty"`
	Model        string                  `json:"model,omitempty"`
	RootCause    model.RootCause         `json:"root_cause"`
	Trigger      model.DiagnosticTrigger `json:"trigger,omitempty"`
	Actor        string                  `json:"actor,omitempty"`
	MaxCostUSD   float64                 `json:"max_cost_usd,omitempty"`
}

type SubmitReport struct {
	Session         *model.DiagnosticSession `json:"session"`
	Accepted        bool                     `json:"accepted"`
	EarlyStop       bool                     `json:"early_stop"`
	ReservedCostUSD float64                  `json:"reserved_cost_usd"`
	Queue           appruntime.QueueStats    `json:"queue"`
}

type PreviewRequest struct {
	ChannelID    int             `json:"channel_id"`
	ChannelKeyID int             `json:"channel_key_id,omitempty"`
	Endpoint     string          `json:"endpoint,omitempty"`
	Model        string          `json:"model,omitempty"`
	RootCause    model.RootCause `json:"root_cause"`
	MaxVariants  int             `json:"max_variants,omitempty"`
}

type PreviewResult struct {
	Plan       VariantPlan                 `json:"plan"`
	Artifacts  []*requestartifact.Artifact `json:"artifacts"`
	ShapeDiffs [][]string                  `json:"shape_diffs"`
}

type Worker struct {
	config   conf.SelfHealing
	executor DiagnosticExecutor
	queue    *appruntime.JobQueue[diagnosticJob]
	limiter  *rate.Limiter
	sentinel *Sentinel

	budgetMu sync.Mutex
	reserved float64
}

func NewWorker(config conf.SelfHealing, executor DiagnosticExecutor) (*Worker, error) {
	diagnostic := config.Diagnostic
	if diagnostic.MaxVariants <= 0 || diagnostic.MaxVariants > MaxDiagnosticVariants ||
		diagnostic.MaxConcurrency <= 0 || diagnostic.QueueDepth <= 0 || diagnostic.RequestsPerMinute <= 0 || diagnostic.TimeoutSeconds <= 0 {
		return nil, errors.New("invalid self-healing diagnostic worker bounds")
	}
	if diagnostic.CostPerRequestUSD <= 0 || diagnostic.MaxBatchCostUSD <= 0 || diagnostic.MaxTotalCostUSD <= 0 {
		return nil, errors.New("invalid self-healing diagnostic cost bounds")
	}
	if executor == nil {
		executor = IsolatedExecutor{}
	}
	worker := &Worker{
		config: config, executor: executor,
		limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(diagnostic.RequestsPerMinute)), diagnostic.MaxConcurrency),
	}
	queue, err := appruntime.NewJobQueue(appruntime.JobQueueConfig[diagnosticJob]{
		Name: "self_healing_diagnostic", QueueDepth: diagnostic.QueueDepth, Concurrency: diagnostic.MaxConcurrency,
		Key:     func(job diagnosticJob) string { return job.SessionID },
		Handle:  worker.handle,
		OnError: func(err error) { log.Errorf("self-healing diagnostic job failed: %v", err) },
	})
	if err != nil {
		return nil, err
	}
	worker.queue = queue
	worker.sentinel = newSentinel(config, worker)
	return worker, nil
}

func InstallDefault(config conf.SelfHealing) (*Worker, error) {
	worker, err := NewWorker(config, nil)
	if err != nil {
		return nil, err
	}
	defaultWorker.Store(worker)
	return worker, nil
}

func DefaultWorker() *Worker { return defaultWorker.Load() }

func (w *Worker) Start(ctx context.Context) error {
	if err := w.queue.Start(ctx); err != nil {
		return err
	}
	if err := w.sentinel.Start(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = w.queue.Stop(stopCtx)
		return err
	}
	return nil
}

func (w *Worker) Stop(ctx context.Context) error {
	var result error
	if w.sentinel != nil {
		result = errors.Join(result, w.sentinel.Stop(ctx))
	}
	result = errors.Join(result, w.queue.Stop(ctx))
	return result
}
func (w *Worker) Stats() appruntime.QueueStats { return w.queue.Stats() }

func (w *Worker) extraCandidates() ExtraCandidates {
	return ExtraCandidates{
		UserAgents: w.config.Diagnostic.ExtraUserAgents,
		Headers:    w.config.Diagnostic.ExtraHeaders,
	}
}

func (w *Worker) Submit(ctx context.Context, request SubmitRequest) (SubmitReport, error) {
	if w == nil || !w.config.Enabled {
		return SubmitReport{}, ErrSelfHealingDisabled
	}
	channel, key, endpoint, modelName, err := resolveDiagnosticScope(ctx, request.ChannelID, request.ChannelKeyID, request.Endpoint, request.Model)
	if err != nil {
		return SubmitReport{}, err
	}
	if !channel.SelfHealingEnabled {
		return SubmitReport{}, ErrChannelSelfHealingDisabled
	}
	if !request.RootCause.Valid() || request.RootCause == model.RootCauseNone {
		return SubmitReport{}, fmt.Errorf("root_cause must identify a diagnostic failure")
	}
	if request.Trigger == "" {
		request.Trigger = model.DiagnosticTriggerManual
	}
	if !request.Trigger.Valid() {
		return SubmitReport{}, errors.New("invalid diagnostic trigger")
	}
	plan := GenerateVariants(channel, channel.Type, request.RootCause, w.config.Diagnostic.MaxVariants, w.extraCandidates())
	maxAttempts := len(plan.Variants)
	if plan.EarlyStop {
		maxAttempts = 1
	}
	reserve := float64(maxAttempts) * w.config.Diagnostic.CostPerRequestUSD
	if plan.EarlyStop {
		reserve = 0
	}
	batchCap := w.config.Diagnostic.MaxBatchCostUSD
	if request.MaxCostUSD < 0 || request.MaxCostUSD > batchCap {
		return SubmitReport{}, fmt.Errorf("max_cost_usd must be between 0 and %.6f", batchCap)
	}
	if request.MaxCostUSD > 0 {
		batchCap = request.MaxCostUSD
	}
	if reserve > batchCap+1e-12 {
		return SubmitReport{}, fmt.Errorf("diagnostic matrix cost %.6f exceeds batch budget %.6f", reserve, batchCap)
	}
	w.budgetMu.Lock()
	if w.reserved+reserve > w.config.Diagnostic.MaxTotalCostUSD+1e-12 {
		w.budgetMu.Unlock()
		return SubmitReport{}, errors.New("self-healing total diagnostic cost budget exhausted")
	}
	w.reserved += reserve
	w.budgetMu.Unlock()

	now := time.Now().UTC()
	session := &model.DiagnosticSession{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: modelName, WireProtocol: channel.Type,
		Endpoint: endpoint, EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint),
		ScopeFingerprint: model.CapabilityScopeFingerprint(channel, key, endpoint), ConfigVersion: channel.ConfigVersion,
		Mode: model.DiagnosticModeLive, Trigger: request.Trigger, Status: model.DiagnosticSessionQueued,
		RootCause: request.RootCause, Actor: request.Actor, MaxAttempts: maxAttempts, ReservedCostUSD: reserve,
		Deadline: now.Add(time.Duration(w.config.Diagnostic.SessionTTLSeconds) * time.Second),
	}
	if err := op.DiagnosticSessionCreate(ctx, session); err != nil {
		w.releaseBudget(reserve)
		return SubmitReport{}, err
	}
	report := SubmitReport{Session: session, ReservedCostUSD: reserve, Queue: w.queue.Stats()}
	if plan.EarlyStop {
		report.EarlyStop = true
		w.releaseBudget(reserve)
		if err := op.DiagnosticSessionFinishUpdate(ctx, session.ID, op.DiagnosticSessionFinish{
			Status: model.DiagnosticSessionCompleted, RootCause: request.RootCause, StopReason: plan.StopReason,
		}); err != nil {
			return report, err
		}
		metrics.RecordSelfHealingDiagnostic(channel.ID, string(request.RootCause), "early_stop")
		if loaded, loadErr := op.DiagnosticSessionGet(ctx, session.ID); loadErr == nil && loaded != nil {
			report.Session = loaded
		}
		return report, nil
	}
	switch result := w.queue.Submit(diagnosticJob{SessionID: session.ID}); result {
	case appruntime.SubmitAccepted:
		report.Accepted = true
		metrics.RecordSelfHealingDiagnostic(channel.ID, string(request.RootCause), "queued")
	case appruntime.SubmitCoalesced:
		_ = op.DiagnosticSessionFinishUpdate(ctx, session.ID, op.DiagnosticSessionFinish{Status: model.DiagnosticSessionFailed, RootCause: request.RootCause, StopReason: "diagnostic job was unexpectedly coalesced"})
		w.releaseBudget(reserve)
		metrics.RecordSelfHealingDiagnostic(channel.ID, string(request.RootCause), "coalesced")
		return report, fmt.Errorf("%w: diagnostic job was coalesced", op.ErrConflict)
	default:
		_ = op.DiagnosticSessionFinishUpdate(ctx, session.ID, op.DiagnosticSessionFinish{Status: model.DiagnosticSessionFailed, RootCause: request.RootCause, StopReason: "diagnostic queue is full"})
		w.releaseBudget(reserve)
		metrics.RecordSelfHealingDiagnostic(channel.ID, string(request.RootCause), "queue_full")
		return report, errors.New("self-healing diagnostic queue is full")
	}
	report.Queue = w.queue.Stats()
	return report, nil
}

func (w *Worker) Preview(ctx context.Context, request PreviewRequest) (*PreviewResult, error) {
	if w == nil || !w.config.Enabled {
		return nil, ErrSelfHealingDisabled
	}
	channel, key, endpoint, modelName, err := resolveDiagnosticScope(ctx, request.ChannelID, request.ChannelKeyID, request.Endpoint, request.Model)
	if err != nil {
		return nil, err
	}
	if !channel.SelfHealingEnabled {
		return nil, ErrChannelSelfHealingDisabled
	}
	if !request.RootCause.Valid() {
		return nil, errors.New("invalid root_cause")
	}
	maxVariants := request.MaxVariants
	if maxVariants <= 0 {
		maxVariants = w.config.Diagnostic.MaxVariants
	}
	plan := GenerateVariants(channel, channel.Type, request.RootCause, maxVariants, w.extraCandidates())
	internalRequest := minimalDiagnosticRequest(channel.Type, modelName)
	result := &PreviewResult{Plan: plan, Artifacts: make([]*requestartifact.Artifact, 0, len(plan.Variants)), ShapeDiffs: make([][]string, 0, len(plan.Variants))}
	for _, variant := range plan.Variants {
		built, buildErr := BuildVariantRequest(ctx, channel, key, endpoint, internalRequest, variant)
		if buildErr != nil {
			return nil, buildErr
		}
		result.Artifacts = append(result.Artifacts, built.Artifact)
		result.ShapeDiffs = append(result.ShapeDiffs, built.ShapeDiff)
	}
	return result, nil
}

func (w *Worker) handle(parent context.Context, job diagnosticJob) error {
	session, err := op.DiagnosticSessionGet(parent, job.SessionID)
	if err != nil {
		return nil
	}
	defer w.releaseBudget(session.ReservedCostUSD)
	if err := op.DiagnosticSessionStart(parent, session.ID, time.Now().UTC()); err != nil {
		return nil
	}
	channel, key, err := currentDiagnosticScope(parent, session)
	if err != nil {
		return w.finishFailed(parent, session, model.RootCauseUnknown, "", err.Error())
	}
	plan := GenerateVariants(channel, session.WireProtocol, session.RootCause, session.MaxAttempts, w.extraCandidates())
	internalRequest := minimalDiagnosticRequest(channel.Type, session.Model)
	baselineAvailable := hasValidDiagnosticBaseline(parent, session, channel, key)
	lastDiagnosis := Diagnosis{RootCause: session.RootCause}
	lastReason := "diagnostic matrix completed without a verified success"
	for _, variant := range plan.Variants {
		if err := w.limiter.Wait(parent); err != nil {
			return w.finishFailed(parent, session, lastDiagnosis.RootCause, lastDiagnosis.Classification.Level.String(), err.Error())
		}
		remaining := time.Until(session.Deadline)
		if remaining <= 0 {
			return w.finishFailed(parent, session, lastDiagnosis.RootCause, lastDiagnosis.Classification.Level.String(), "diagnostic session deadline reached")
		}
		timeout := min(remaining, time.Duration(w.config.Diagnostic.TimeoutSeconds)*time.Second)
		attemptCtx, cancel := context.WithTimeout(parent, timeout)
		attempt := &model.DiagnosticAttempt{
			SessionID: session.ID, VariantID: variant.ID, ParentVariantID: variant.ParentVariantID,
			ChangedDimension: variant.Dimension, Status: model.DiagnosticAttemptRunning,
			RequestShape: requestartifact.Artifact{ShapeSHA256: "pending"}, RootCause: model.RootCauseUnknown, StartedAt: time.Now().UTC(),
		}
		if err := op.DiagnosticAttemptCreate(attemptCtx, attempt); err != nil {
			cancel()
			return w.finishFailed(parent, session, lastDiagnosis.RootCause, lastDiagnosis.Classification.Level.String(), err.Error())
		}
		metrics.IncSelfHealingProbeInFlight()
		result := w.executor.Execute(attemptCtx, channel, key, session.Endpoint, internalRequest, variant)
		metrics.DecSelfHealingProbeInFlight()
		cancel()
		lastDiagnosis = result.Classification
		lastReason = result.Classification.Classification.Reason
		attemptStatus := model.DiagnosticAttemptFailed
		variantResult := "failed"
		if result.Success {
			attemptStatus = model.DiagnosticAttemptSuccess
			variantResult = "success"
		}
		metrics.RecordSelfHealingVariant(variant.Dimension, variantResult)
		if result.Artifact != nil {
			if err := op.DiagnosticAttemptSetRequestShape(parent, attempt.ID, result.Artifact); err != nil {
				return w.finishFailed(parent, session, lastDiagnosis.RootCause, lastDiagnosis.Classification.Level.String(), err.Error())
			}
		}
		if err := op.DiagnosticAttemptFinishUpdate(parent, attempt.ID, op.DiagnosticAttemptFinish{
			Status: attemptStatus, ResponseHeaders: result.ResponseHeaders, HTTPStatus: result.HTTPStatus,
			ErrorLevel: result.Classification.Classification.Level.String(), RootCause: result.Classification.RootCause,
			ErrorReason: result.Classification.Classification.Reason, ShapeDiff: result.ShapeDiff,
			ResponseFingerprint: result.ResponseFingerprint, Success: result.Success, DurationMS: result.Duration.Milliseconds(),
			CostUSD: w.config.Diagnostic.CostPerRequestUSD,
		}); err != nil {
			return w.finishFailed(parent, session, lastDiagnosis.RootCause, lastDiagnosis.Classification.Level.String(), err.Error())
		}
		if result.Success {
			w.persistDiagnosticBaseline(parent, session, channel, key, result)
			attempts, listErr := op.DiagnosticAttemptList(parent, session.ID)
			if listErr != nil {
				return w.finishFailed(parent, session, model.RootCauseUnknown, "", listErr.Error())
			}
			if baselineAvailable {
				if patch, patchErr := GenerateCandidatePatch(session, channel, plan, attempts); patchErr == nil {
					if createErr := op.ChannelPatchCreate(parent, patch); createErr != nil {
						return w.finishFailed(parent, session, model.RootCauseUnknown, "", createErr.Error())
					}
				} else if !errors.Is(patchErr, ErrNoPatchCandidate) {
					return w.finishFailed(parent, session, model.RootCauseUnknown, "", patchErr.Error())
				}
			}
			err := op.DiagnosticSessionFinishUpdate(parent, session.ID, op.DiagnosticSessionFinish{
				Status: model.DiagnosticSessionCompleted, RootCause: model.RootCauseNone,
				ErrorLevel: result.Classification.Classification.Level.String(), StopReason: "verified diagnostic request succeeded",
			})
			metrics.RecordSelfHealingDiagnostic(session.ChannelID, string(session.RootCause), "completed_success")
			return err
		}
		if !result.Classification.PatchEligible() {
			err := op.DiagnosticSessionFinishUpdate(parent, session.ID, op.DiagnosticSessionFinish{
				Status: model.DiagnosticSessionCompleted, RootCause: result.Classification.RootCause,
				ErrorLevel: result.Classification.Classification.Level.String(), ErrorReason: lastReason,
				StopReason: "non-patchable root cause stopped the diagnostic matrix",
			})
			metrics.RecordSelfHealingDiagnostic(session.ChannelID, string(result.Classification.RootCause), "completed_non_patchable")
			return err
		}
	}
	err = op.DiagnosticSessionFinishUpdate(parent, session.ID, op.DiagnosticSessionFinish{
		Status: model.DiagnosticSessionCompleted, RootCause: lastDiagnosis.RootCause,
		ErrorLevel: lastDiagnosis.Classification.Level.String(), ErrorReason: lastReason,
		StopReason: "no diagnostic variant produced a verified success",
	})
	metrics.RecordSelfHealingDiagnostic(session.ChannelID, string(lastDiagnosis.RootCause), "completed_no_success")
	return err
}

func hasValidDiagnosticBaseline(ctx context.Context, session *model.DiagnosticSession, channel *model.Channel, key model.ChannelKey) bool {
	if session == nil || channel == nil || key.ID <= 0 {
		return false
	}
	_, err := op.ChannelBaselineLatest(ctx, channel.ID, key.ID, session.Model, string(channel.Type),
		session.EndpointFingerprint, session.ScopeFingerprint)
	return err == nil
}

func (w *Worker) finishFailed(ctx context.Context, session *model.DiagnosticSession, rootCause model.RootCause, errorLevel, reason string) error {
	if !rootCause.Valid() {
		rootCause = model.RootCauseUnknown
	}
	err := op.DiagnosticSessionFinishUpdate(ctx, session.ID, op.DiagnosticSessionFinish{
		Status: model.DiagnosticSessionFailed, RootCause: rootCause, ErrorLevel: errorLevel, ErrorReason: reason, StopReason: reason,
	})
	metrics.RecordSelfHealingDiagnostic(session.ChannelID, string(rootCause), "failed")
	return err
}

func (w *Worker) persistDiagnosticBaseline(ctx context.Context, session *model.DiagnosticSession, channel *model.Channel, key model.ChannelKey, result AttemptResult) {
	if result.Artifact == nil {
		return
	}
	now := time.Now().UTC()
	baseline := &model.ChannelBaseline{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: session.Model, WireProtocol: channel.Type,
		Endpoint: session.Endpoint, EndpointFingerprint: session.EndpointFingerprint, ScopeFingerprint: session.ScopeFingerprint,
		RequestShape: *result.Artifact, HTTPStatus: result.HTTPStatus,
		ContentType: firstHeader(result.ResponseHeaders, "content-type"), Source: model.ChannelBaselineSourceManualLive,
		CapturedAt: now, ExpiresAt: now.Add(time.Duration(w.config.BaselineTTLSeconds) * time.Second), Version: 1,
	}
	if err := op.ChannelBaselineCreate(ctx, baseline); err != nil {
		log.Warnf("persist successful diagnostic baseline failed: %v", err)
	}
}

func (w *Worker) releaseBudget(amount float64) {
	w.budgetMu.Lock()
	w.reserved = max(0, w.reserved-amount)
	w.budgetMu.Unlock()
}

func resolveDiagnosticScope(ctx context.Context, channelID, keyID int, endpoint, modelName string) (*model.Channel, model.ChannelKey, string, string, error) {
	if channelID <= 0 {
		return nil, model.ChannelKey{}, "", "", errors.New("channel_id must be positive")
	}
	channel, err := op.ChannelGet(channelID, ctx)
	if err != nil {
		return nil, model.ChannelKey{}, "", "", err
	}
	key, err := selectDiagnosticKey(channel, keyID)
	if err != nil {
		return nil, model.ChannelKey{}, "", "", err
	}
	endpoint, err = selectDiagnosticEndpoint(channel, endpoint)
	if err != nil {
		return nil, model.ChannelKey{}, "", "", err
	}
	modelName, err = selectDiagnosticModel(channel, modelName)
	if err != nil {
		return nil, model.ChannelKey{}, "", "", err
	}
	return channel, key, endpoint, modelName, nil
}

func currentDiagnosticScope(ctx context.Context, session *model.DiagnosticSession) (*model.Channel, model.ChannelKey, error) {
	channel, key, endpoint, modelName, err := resolveDiagnosticScope(ctx, session.ChannelID, session.ChannelKeyID, session.Endpoint, session.Model)
	if err != nil {
		return nil, model.ChannelKey{}, err
	}
	if endpoint != session.Endpoint || modelName != session.Model || channel.ConfigVersion != session.ConfigVersion ||
		model.CapabilityScopeFingerprint(channel, key, endpoint) != session.ScopeFingerprint {
		return nil, model.ChannelKey{}, errors.New("channel configuration changed after diagnostic session creation")
	}
	return channel, key, nil
}

func selectDiagnosticKey(channel *model.Channel, keyID int) (model.ChannelKey, error) {
	for _, key := range channel.Keys {
		if (keyID == 0 || key.ID == keyID) && key.Enabled && strings.TrimSpace(key.ChannelKey) != "" {
			return key, nil
		}
	}
	return model.ChannelKey{}, errors.New("channel has no matching enabled diagnostic key")
}

func selectDiagnosticEndpoint(channel *model.Channel, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	for _, item := range channel.BaseUrls {
		endpoint := strings.TrimSpace(item.URL)
		if endpoint != "" && (requested == "" || requested == endpoint) {
			return endpoint, nil
		}
	}
	return "", errors.New("channel has no matching diagnostic endpoint")
}

func selectDiagnosticModel(channel *model.Channel, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	for _, name := range xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel) {
		if requested == "" || requested == name {
			return name, nil
		}
	}
	return "", errors.New("channel has no matching diagnostic model")
}

func minimalDiagnosticRequest(protocol llm.APIFormat, modelName string) *llm.Request {
	prompt := "Reply with OK."
	stream := false
	maxTokens := DiagnosticMaxOutputTokens
	request := &llm.Request{
		Messages: []llm.Message{{Role: "user", Content: llm.MessageContent{Content: &prompt}}},
		Model:    modelName, MaxTokens: &maxTokens, Stream: &stream, RequestType: llm.RequestTypeChat, APIFormat: protocol,
	}
	quotedModel := strconv.Quote(modelName)
	var rawBody []byte
	switch protocol {
	case llm.APIFormatOpenAIResponse, model.ChannelTypeOpenAICodex:
		rawBody = []byte(fmt.Sprintf(`{"model":%s,"input":"Reply with OK.","max_output_tokens":8,"stream":false}`, quotedModel))
	case llm.APIFormatAnthropicMessage:
		rawBody = []byte(fmt.Sprintf(`{"model":%s,"messages":[{"role":"user","content":"Reply with OK."}],"max_tokens":8,"stream":false}`, quotedModel))
	case llm.APIFormatGeminiContents:
		rawBody = []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with OK."}]}],"generationConfig":{"maxOutputTokens":8}}`)
	default:
		rawBody = []byte(fmt.Sprintf(`{"model":%s,"messages":[{"role":"user","content":"Reply with OK."}],"max_tokens":8,"stream":false}`, quotedModel))
	}
	request.RawRequest = &httpclient.Request{Method: "POST", ContentType: "application/json", Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: rawBody}
	return request
}

func firstHeader(headers map[string][]string, name string) string {
	values := headers[strings.ToLower(name)]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
