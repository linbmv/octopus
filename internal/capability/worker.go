package capability

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
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	appruntime "github.com/bestruirui/octopus/internal/runtime"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
	"golang.org/x/time/rate"
)

const maxProbeModelsPerBatch = 100

var (
	ErrProbeDisabled = errors.New("capability probing is disabled")
	defaultWorker    atomic.Pointer[Worker]
)

type Job struct {
	ChannelID    int
	ChannelKeyID int
	Endpoint     string
	Model        string
	Capability   model.Capability
}

type SubmitRequest struct {
	ChannelID    int                `json:"channel_id"`
	Models       []string           `json:"models,omitempty"`
	Capabilities []model.Capability `json:"capabilities,omitempty"`
	MaxCostUSD   float64            `json:"max_cost_usd,omitempty"`
}

type SubmitReport struct {
	Requested        int                   `json:"requested"`
	Accepted         int                   `json:"accepted"`
	Coalesced        int                   `json:"coalesced"`
	Dropped          int                   `json:"dropped"`
	BudgetRejected   int                   `json:"budget_rejected"`
	ReservedCostUSD  float64               `json:"reserved_cost_usd"`
	TotalReservedUSD float64               `json:"total_reserved_usd"`
	RemainingCostUSD float64               `json:"remaining_cost_usd"`
	Queue            appruntime.QueueStats `json:"queue"`
}

type Worker struct {
	config  conf.CapabilityProbe
	prober  Prober
	queue   *appruntime.JobQueue[Job]
	limiter *rate.Limiter

	budgetMu sync.Mutex
	reserved float64
}

func NewWorker(config conf.CapabilityProbe, prober Prober) (*Worker, error) {
	if prober == nil {
		prober = HTTPProber{}
	}
	if config.RequestsPerMinute <= 0 || config.MaxConcurrency <= 0 || config.QueueDepth <= 0 {
		return nil, errors.New("invalid capability probe worker bounds")
	}
	if config.CostPerProbeUSD <= 0 || config.MaxBatchCostUSD <= 0 || config.MaxTotalCostUSD <= 0 {
		return nil, errors.New("invalid capability probe cost bounds")
	}
	worker := &Worker{
		config:  config,
		prober:  prober,
		limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(config.RequestsPerMinute)), config.MaxConcurrency),
	}
	queue, err := appruntime.NewJobQueue(appruntime.JobQueueConfig[Job]{
		Name:        "capability_probe",
		QueueDepth:  config.QueueDepth,
		Concurrency: config.MaxConcurrency,
		Key: func(job Job) string {
			return strconv.Itoa(job.ChannelID) + ":" + strconv.Itoa(job.ChannelKeyID) + ":" +
				model.CapabilityEndpointFingerprint(job.Endpoint) + ":" + job.Model + ":" + string(job.Capability)
		},
		Handle:  worker.handle,
		OnError: func(err error) { log.Errorf("capability probe job failed: %v", err) },
	})
	if err != nil {
		return nil, err
	}
	worker.queue = queue
	return worker, nil
}

func InstallDefault(config conf.CapabilityProbe) (*Worker, error) {
	worker, err := NewWorker(config, nil)
	if err != nil {
		return nil, err
	}
	defaultWorker.Store(worker)
	return worker, nil
}

func DefaultWorker() *Worker { return defaultWorker.Load() }

func (w *Worker) Start(ctx context.Context) error { return w.queue.Start(ctx) }

func (w *Worker) Stop(ctx context.Context) error { return w.queue.Stop(ctx) }

func (w *Worker) Stats() appruntime.QueueStats { return w.queue.Stats() }

func (w *Worker) Submit(ctx context.Context, request SubmitRequest) (SubmitReport, error) {
	if w == nil || !w.config.Enabled {
		return SubmitReport{}, ErrProbeDisabled
	}
	if request.ChannelID <= 0 {
		return SubmitReport{}, errors.New("channel_id must be positive")
	}
	channel, err := op.ChannelGet(request.ChannelID, ctx)
	if err != nil {
		return SubmitReport{}, fmt.Errorf("get probe channel: %w", err)
	}
	models, err := selectedModels(channel, request.Models)
	if err != nil {
		return SubmitReport{}, err
	}
	capabilities, err := selectedCapabilities(request.Capabilities)
	if err != nil {
		return SubmitReport{}, err
	}
	keys := enabledProbeKeys(channel.Keys)
	if len(keys) == 0 {
		return SubmitReport{}, errors.New("channel has no enabled API key")
	}
	endpoints := probeEndpoints(channel.BaseUrls)
	if len(endpoints) == 0 {
		return SubmitReport{}, errors.New("channel has no probe endpoint")
	}

	batchCap := w.config.MaxBatchCostUSD
	if request.MaxCostUSD < 0 || request.MaxCostUSD > w.config.MaxBatchCostUSD {
		return SubmitReport{}, fmt.Errorf("max_cost_usd must be between 0 and %.6f", w.config.MaxBatchCostUSD)
	}
	if request.MaxCostUSD > 0 {
		batchCap = request.MaxCostUSD
	}

	report := SubmitReport{Requested: len(models) * len(capabilities) * len(keys) * len(endpoints)}
	w.budgetMu.Lock()
	defer w.budgetMu.Unlock()
	batchReserved := 0.0
	for _, endpoint := range endpoints {
		for _, key := range keys {
			for _, modelName := range models {
				for _, capability := range capabilities {
					if batchReserved+w.config.CostPerProbeUSD > batchCap+1e-12 ||
						w.reserved+w.config.CostPerProbeUSD > w.config.MaxTotalCostUSD+1e-12 {
						report.BudgetRejected++
						continue
					}
					result := w.queue.Submit(Job{
						ChannelID: channel.ID, ChannelKeyID: key.ID, Endpoint: endpoint,
						Model: modelName, Capability: capability,
					})
					switch result {
					case appruntime.SubmitAccepted:
						report.Accepted++
						batchReserved += w.config.CostPerProbeUSD
						w.reserved += w.config.CostPerProbeUSD
					case appruntime.SubmitCoalesced:
						report.Coalesced++
					default:
						report.Dropped++
					}
				}
			}
		}
	}
	report.ReservedCostUSD = batchReserved
	report.TotalReservedUSD = w.reserved
	report.RemainingCostUSD = max(0, w.config.MaxTotalCostUSD-w.reserved)
	report.Queue = w.queue.Stats()
	return report, nil
}

func (w *Worker) handle(parent context.Context, job Job) error {
	if err := w.limiter.Wait(parent); err != nil {
		return err
	}
	channel, err := op.ChannelGet(job.ChannelID, parent)
	if err != nil {
		return nil // Deleted channels invalidate queued work.
	}
	key, ok := currentProbeKey(channel.Keys, job.ChannelKeyID)
	if !ok || !endpointStillConfigured(channel.BaseUrls, job.Endpoint) || !modelStillConfigured(channel, job.Model) {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(w.config.TimeoutSeconds)*time.Second)
	defer cancel()
	result := w.prober.Probe(ctx, channel, key, job.Endpoint, job.Model, job.Capability, w.config.MaxOutputTokens)
	if key.ChannelKey != "" {
		result.ErrorMessage = strings.ReplaceAll(result.ErrorMessage, key.ChannelKey, "[redacted]")
	}
	now := time.Now().UTC()
	evidence := model.CapabilityEvidence{
		ChannelID:           channel.ID,
		ChannelKeyID:        key.ID,
		Model:               job.Model,
		WireProtocol:        channel.Type,
		Capability:          job.Capability,
		Endpoint:            job.Endpoint,
		EndpointFingerprint: model.CapabilityEndpointFingerprint(job.Endpoint),
		Status:              result.Status,
		ErrorClass:          result.ErrorClass,
		ErrorMessage:        result.ErrorMessage,
		HTTPStatus:          result.HTTPStatus,
		ScopeFingerprint:    model.CapabilityScopeFingerprint(channel, key, job.Endpoint),
		Source:              "probe",
		ProbedAt:            now,
		ExpiresAt:           now.Add(time.Duration(w.config.TTLSeconds) * time.Second),
	}
	return op.CapabilityEvidenceUpsert(ctx, &evidence)
}

func selectedModels(channel *model.Channel, requested []string) ([]string, error) {
	configured := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
	allowed := make(map[string]struct{}, len(configured))
	for _, name := range configured {
		allowed[name] = struct{}{}
	}
	if len(requested) == 0 {
		requested = configured
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > model.MaxModelNameBytes {
			return nil, errors.New("models contains an invalid model name")
		}
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("model %q is not configured on channel", name)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, errors.New("channel has no configured model to probe")
	}
	if len(result) > maxProbeModelsPerBatch {
		return nil, fmt.Errorf("one probe batch may contain at most %d models", maxProbeModelsPerBatch)
	}
	return result, nil
}

func selectedCapabilities(requested []model.Capability) ([]model.Capability, error) {
	if len(requested) == 0 {
		return []model.Capability{model.CapabilityText, model.CapabilityStream, model.CapabilityTool, model.CapabilityVision}, nil
	}
	result := make([]model.Capability, 0, len(requested))
	seen := make(map[model.Capability]struct{}, len(requested))
	for _, capability := range requested {
		if !capability.Valid() {
			return nil, fmt.Errorf("invalid capability %q", capability)
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		result = append(result, capability)
	}
	return result, nil
}

func enabledProbeKeys(keys []model.ChannelKey) []model.ChannelKey {
	result := make([]model.ChannelKey, 0, len(keys))
	for _, key := range keys {
		if key.ID > 0 && key.Enabled && strings.TrimSpace(key.ChannelKey) != "" {
			result = append(result, key)
		}
	}
	return result
}

func probeEndpoints(baseURLs []model.BaseUrl) []string {
	result := make([]string, 0, len(baseURLs))
	seen := make(map[string]struct{}, len(baseURLs))
	for _, item := range baseURLs {
		endpoint := strings.TrimSpace(item.URL)
		if endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		result = append(result, endpoint)
	}
	return result
}

func currentProbeKey(keys []model.ChannelKey, keyID int) (model.ChannelKey, bool) {
	for _, key := range keys {
		if key.ID == keyID && key.Enabled && strings.TrimSpace(key.ChannelKey) != "" {
			return key, true
		}
	}
	return model.ChannelKey{}, false
}

func endpointStillConfigured(baseURLs []model.BaseUrl, endpoint string) bool {
	for _, item := range baseURLs {
		if strings.TrimSpace(item.URL) == endpoint {
			return true
		}
	}
	return false
}

func modelStillConfigured(channel *model.Channel, modelName string) bool {
	for _, configured := range xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel) {
		if configured == modelName {
			return true
		}
	}
	return false
}
