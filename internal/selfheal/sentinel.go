package selfheal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/utils/log"
)

const sentinelSignalQueueDepth = 128

type sentinelFailureKey struct {
	ChannelID           int
	ChannelKeyID        int
	Model               string
	EndpointFingerprint string
	RootCause           model.RootCause
}

type sentinelFailureSignal struct {
	ChannelID           int
	ChannelKeyID        int
	Model               string
	Endpoint            string
	EndpointFingerprint string
	RootCause           model.RootCause
	ErrorLevel          string
	Reason              string
	ObservedAt          time.Time
}

// Sentinel aggregates production failures without changing relay routing. It
// receives bounded observations from the relay process and persists only
// diagnostic sessions created by the normal Worker gate/budget checks.
type Sentinel struct {
	config conf.SelfHealing
	worker *Worker

	mu             sync.Mutex
	failures       map[sentinelFailureKey][]time.Time
	cooldowns      map[sentinelFailureKey]time.Time
	signals        chan sentinelFailureSignal
	cancel         context.CancelFunc
	done           chan struct{}
	removeObserver func()
	started        bool
}

func newSentinel(config conf.SelfHealing, worker *Worker) *Sentinel {
	return &Sentinel{
		config: config, worker: worker,
		failures:  make(map[sentinelFailureKey][]time.Time),
		cooldowns: make(map[sentinelFailureKey]time.Time),
		signals:   make(chan sentinelFailureSignal, sentinelSignalQueueDepth),
	}
}

func (s *Sentinel) Start(parent context.Context) error {
	if s == nil {
		return errors.New("self-healing sentinel is nil")
	}
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	done := s.done
	s.removeObserver = relay.RegisterUpstreamFailureObserver(s.observeRelayFailure)
	s.mu.Unlock()
	go s.run(ctx, done)
	return nil
}

func (s *Sentinel) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	cancel := s.cancel
	done := s.done
	remove := s.removeObserver
	s.cancel = nil
	s.done = nil
	s.removeObserver = nil
	s.mu.Unlock()
	if remove != nil {
		remove()
	}
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Observe is intentionally non-blocking. It is useful for deterministic
// tests and is also the callback target used by relay's in-process observer.
func (s *Sentinel) Observe(observation relay.UpstreamFailureObservation) {
	if s == nil || !s.config.Enabled || observation.ChannelID <= 0 {
		return
	}
	diagnosis := Classify(Observation{
		HTTPStatus: observation.HTTPStatus, Headers: observation.Headers,
		Body: observation.ResponseBody, TransportError: observation.TransportError,
	})
	if !diagnosis.RootCause.Valid() || diagnosis.RootCause == model.RootCauseNone {
		return
	}
	signal := sentinelFailureSignal{
		ChannelID: observation.ChannelID, ChannelKeyID: observation.ChannelKeyID,
		Model: observation.Model, Endpoint: observation.Endpoint,
		EndpointFingerprint: model.CapabilityEndpointFingerprint(observation.Endpoint),
		RootCause:           diagnosis.RootCause, ErrorLevel: diagnosis.Classification.Level.String(),
		Reason: boundedReason(diagnosis.Classification.Reason), ObservedAt: observation.ObservedAt,
	}
	if signal.ObservedAt.IsZero() {
		signal.ObservedAt = time.Now().UTC()
	}
	select {
	case s.signals <- signal:
	default:
		// A full sentinel queue must not add latency or failure to production.
	}
}

func (s *Sentinel) observeRelayFailure(observation relay.UpstreamFailureObservation) {
	s.Observe(observation)
}

func (s *Sentinel) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	interval := time.Duration(s.config.SentinelIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case signal := <-s.signals:
			s.handleSignal(ctx, signal)
		case now := <-ticker.C:
			s.cleanup(ctx, now.UTC())
		}
	}
}

func (s *Sentinel) handleSignal(ctx context.Context, signal sentinelFailureSignal) {
	if !signal.RootCause.Valid() || signal.RootCause == model.RootCauseNone || signal.ChannelID <= 0 || signal.ChannelKeyID <= 0 {
		return
	}
	if signal.RootCause != model.RootCauseProtocolDrift && signal.RootCause != model.RootCauseWAFOrClientFingerprint {
		// Capacity, rate-limit, auth, endpoint and network failures are useful
		// evidence but must not create a configuration patch or spend probe cost.
		metrics.RecordSelfHealingSentinel(signal.ChannelID, "ignored_"+string(signal.RootCause))
		return
	}
	now := signal.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	window := time.Duration(s.config.FailureWindowSeconds) * time.Second
	if window <= 0 {
		window = 5 * time.Minute
	}
	key := sentinelFailureKey{ChannelID: signal.ChannelID, ChannelKeyID: signal.ChannelKeyID,
		Model: signal.Model, EndpointFingerprint: signal.EndpointFingerprint, RootCause: signal.RootCause}
	s.mu.Lock()
	entries := s.failures[key]
	cutoff := now.Add(-window)
	filtered := entries[:0]
	for _, at := range entries {
		if !at.Before(cutoff) {
			filtered = append(filtered, at)
		}
	}
	if until := s.cooldowns[key]; until.After(now) {
		s.failures[key] = filtered
		s.mu.Unlock()
		return
	}
	filtered = append(filtered, now)
	threshold := s.config.FailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	trigger := len(filtered) >= threshold
	if trigger {
		s.failures[key] = nil
		s.cooldowns[key] = now.Add(window)
	} else {
		s.failures[key] = filtered
	}
	s.mu.Unlock()
	if trigger {
		s.submit(ctx, signal)
	}
}

func (s *Sentinel) submit(ctx context.Context, signal sentinelFailureSignal) {
	channel, err := op.ChannelGet(signal.ChannelID, ctx)
	if err != nil || channel == nil || !channel.SelfHealingEnabled {
		return
	}
	key, err := selectDiagnosticKey(channel, signal.ChannelKeyID)
	if err != nil {
		return
	}
	endpoint, err := selectDiagnosticEndpoint(channel, signal.Endpoint)
	if err != nil {
		return
	}
	modelName, err := selectDiagnosticModel(channel, signal.Model)
	if err != nil {
		return
	}
	scopeFingerprint := model.CapabilityScopeFingerprint(channel, key, endpoint)
	if _, err := op.ChannelBaselineLatest(ctx, channel.ID, key.ID, modelName, string(channel.Type),
		model.CapabilityEndpointFingerprint(endpoint), scopeFingerprint); err != nil {
		// A sentinel diagnosis without a verified successful baseline is not
		// allowed to produce a candidate patch.
		metrics.RecordSelfHealingSentinel(signal.ChannelID, "no_baseline")
		return
	}
	if _, err := s.worker.Submit(ctx, SubmitRequest{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Endpoint: endpoint, Model: modelName,
		RootCause: signal.RootCause, Trigger: model.DiagnosticTriggerSentinel, Actor: "sentinel",
	}); err != nil {
		if errors.Is(err, op.ErrConflict) {
			metrics.RecordSelfHealingSentinel(signal.ChannelID, "conflict")
			return
		}
		metrics.RecordSelfHealingSentinel(signal.ChannelID, "submit_failed")
		log.Warnf("self-healing sentinel diagnostic submission failed: channel=%d model=%s root_cause=%s error=%v",
			signal.ChannelID, modelName, signal.RootCause, boundedReason(err.Error()))
		return
	}
	metrics.RecordSelfHealingSentinel(signal.ChannelID, "submitted")
}

func (s *Sentinel) cleanup(ctx context.Context, now time.Time) {
	s.mu.Lock()
	window := time.Duration(s.config.FailureWindowSeconds) * time.Second
	if window <= 0 {
		window = 5 * time.Minute
	}
	cutoff := now.Add(-window)
	for key, entries := range s.failures {
		filtered := entries[:0]
		for _, at := range entries {
			if !at.Before(cutoff) {
				filtered = append(filtered, at)
			}
		}
		if len(filtered) == 0 {
			delete(s.failures, key)
		} else {
			s.failures[key] = filtered
		}
		if until := s.cooldowns[key]; !until.After(now) {
			delete(s.cooldowns, key)
		}
	}
	s.mu.Unlock()
	if err := op.SelfHealingCleanup(ctx, now); err != nil {
		log.Warnf("self-healing session cleanup failed: %s", boundedReason(err.Error()))
	}
	if err := op.ChannelBaselineCleanup(ctx, now); err != nil {
		log.Warnf("self-healing baseline cleanup failed: %s", boundedReason(err.Error()))
	}
}

func (s *Sentinel) String() string {
	if s == nil {
		return "self-healing sentinel <nil>"
	}
	return fmt.Sprintf("self-healing sentinel interval=%ds threshold=%d", s.config.SentinelIntervalSeconds, s.config.FailureThreshold)
}
