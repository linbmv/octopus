package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/codexauth"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

const (
	codexAutoRefreshBefore      = 5 * time.Minute
	codexAutoRefreshReconcile   = time.Minute
	codexAutoRefreshRetry       = 5 * time.Minute
	codexAutoRefreshTimeout     = 30 * time.Second
	codexAutoRefreshConcurrency = 4
)

type codexRefreshListFunc func(context.Context) ([]dbmodel.Channel, error)
type codexRefreshFunc func(context.Context, *dbmodel.Channel, dbmodel.ChannelKey, time.Duration) error

type codexRefreshRetryState struct {
	signature [32]byte
	retryAt   time.Time
}

type codexRefreshTarget struct {
	channel   dbmodel.Channel
	key       dbmodel.ChannelKey
	signature [32]byte
}

// CodexOAuthRefreshWorker refreshes enabled Codex OAuth credentials according
// to their actual expiration time. Request-time refresh remains as a final
// guard for clock drift, newly imported credentials, and worker outages.
type CodexOAuthRefreshWorker struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	list              codexRefreshListFunc
	refresh           codexRefreshFunc
	now               func() time.Time
	refreshBefore     time.Duration
	reconcileInterval time.Duration
	retryInterval     time.Duration
	refreshTimeout    time.Duration
	concurrency       int
	retries           map[int]codexRefreshRetryState
}

var defaultCodexOAuthRefreshWorker = newCodexOAuthRefreshWorker()

func newCodexOAuthRefreshWorker() *CodexOAuthRefreshWorker {
	return &CodexOAuthRefreshWorker{
		list:              op.ChannelList,
		refresh:           refreshCodexOAuthCredential,
		now:               time.Now,
		refreshBefore:     codexAutoRefreshBefore,
		reconcileInterval: codexAutoRefreshReconcile,
		retryInterval:     codexAutoRefreshRetry,
		refreshTimeout:    codexAutoRefreshTimeout,
		concurrency:       codexAutoRefreshConcurrency,
		retries:           make(map[int]codexRefreshRetryState),
	}
}

func DefaultCodexOAuthRefreshWorker() *CodexOAuthRefreshWorker {
	return defaultCodexOAuthRefreshWorker
}

func (w *CodexOAuthRefreshWorker) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return nil
	}
	if w.done != nil {
		select {
		case <-w.done:
			w.cancel = nil
			w.done = nil
		default:
			return errors.New("codex OAuth refresh worker is still stopping")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	w.cancel = cancel
	w.done = done
	w.started = true
	go func() {
		defer close(done)
		w.run(ctx)
	}()
	return nil
}

func (w *CodexOAuthRefreshWorker) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.started = false
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		w.mu.Lock()
		if w.done == done {
			w.cancel = nil
			w.done = nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *CodexOAuthRefreshWorker) run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			delay := w.runCycle(ctx)
			if delay <= 0 {
				delay = time.Second
			}
			timer.Reset(delay)
		}
	}
}

func (w *CodexOAuthRefreshWorker) runCycle(ctx context.Context) time.Duration {
	channels, err := w.list(ctx)
	if err != nil {
		log.Errorf("list Codex OAuth credentials for auto-refresh: %v", err)
		return w.retryInterval
	}
	now := w.now()
	targets, _ := w.plan(channels, now)
	if len(targets) > 0 {
		w.refreshTargets(ctx, targets, now)
		channels, err = w.list(ctx)
		if err != nil {
			log.Errorf("reload Codex OAuth credentials after auto-refresh: %v", err)
			return w.retryInterval
		}
	}
	_, delay := w.plan(channels, w.now())
	return delay
}

func (w *CodexOAuthRefreshWorker) plan(channels []dbmodel.Channel, now time.Time) ([]codexRefreshTarget, time.Duration) {
	reconcile := w.reconcileInterval
	if reconcile <= 0 {
		reconcile = codexAutoRefreshReconcile
	}
	nextWake := now.Add(reconcile)
	targets := make([]codexRefreshTarget, 0)
	active := make(map[int][32]byte)

	for i := range channels {
		channel := channels[i]
		if !channel.Enabled || channel.Type != dbmodel.ChannelTypeOpenAICodex {
			continue
		}
		for _, key := range channel.Keys {
			if key.ID <= 0 || !key.Enabled || strings.TrimSpace(key.ChannelKey) == "" {
				continue
			}
			document, err := codexauth.Parse(key.ChannelKey)
			if err != nil {
				log.Errorf("skip invalid Codex OAuth credential channel=%d key=%d: %v", channel.ID, key.ID, err)
				continue
			}
			credentials := document.Credentials()
			if credentials == nil || strings.TrimSpace(credentials.RefreshToken) == "" {
				continue
			}

			signature := codexProviderSignature(&channel, key.ChannelKey)
			active[key.ID] = signature
			dueAt := now
			if !credentials.ExpiresAt.IsZero() {
				dueAt = credentials.ExpiresAt.Add(-w.refreshBefore)
			}
			if retry, ok := w.retries[key.ID]; ok && retry.signature == signature && retry.retryAt.After(dueAt) {
				dueAt = retry.retryAt
			}
			if dueAt.After(now) {
				if dueAt.Before(nextWake) {
					nextWake = dueAt
				}
				continue
			}
			targets = append(targets, codexRefreshTarget{channel: channel, key: key, signature: signature})
		}
	}

	for keyID, retry := range w.retries {
		if signature, ok := active[keyID]; !ok || signature != retry.signature {
			delete(w.retries, keyID)
		}
	}
	delay := nextWake.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return targets, delay
}

func (w *CodexOAuthRefreshWorker) refreshTargets(ctx context.Context, targets []codexRefreshTarget, now time.Time) {
	concurrency := w.concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	semaphore := make(chan struct{}, concurrency)
	type result struct {
		target codexRefreshTarget
		err    error
	}
	results := make(chan result, len(targets))
	var wait sync.WaitGroup
	for _, target := range targets {
		target := target
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- result{target: target, err: ctx.Err()}
				return
			}
			refreshCtx, cancel := context.WithTimeout(ctx, w.refreshTimeout)
			defer cancel()
			err := w.refresh(refreshCtx, &target.channel, target.key, w.refreshBefore)
			results <- result{target: target, err: err}
		}()
	}
	wait.Wait()
	close(results)

	for item := range results {
		if item.err != nil {
			retryInterval := w.retryInterval
			if retryInterval <= 0 {
				retryInterval = codexAutoRefreshRetry
			}
			w.retries[item.target.key.ID] = codexRefreshRetryState{
				signature: item.target.signature,
				retryAt:   now.Add(retryInterval),
			}
			log.Errorf("Codex OAuth auto-refresh failed channel=%d key=%d: %v", item.target.channel.ID, item.target.key.ID, item.err)
			continue
		}
		delete(w.retries, item.target.key.ID)
		log.Infof("Codex OAuth credential refreshed channel=%d key=%d", item.target.channel.ID, item.target.key.ID)
	}
}

func refreshCodexOAuthCredential(ctx context.Context, channel *dbmodel.Channel, key dbmodel.ChannelKey, refreshBefore time.Duration) error {
	provider, _, err := codexProviderForChannel(channel, key)
	if err != nil {
		return err
	}
	refreshingProvider, ok := provider.(*codexTokenProvider)
	if !ok {
		return fmt.Errorf("unexpected Codex OAuth token provider %T", provider)
	}
	_, err = refreshingProvider.ensureFresh(ctx, refreshBefore)
	return err
}
