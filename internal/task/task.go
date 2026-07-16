package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
)

type taskFunc func(context.Context) error

type taskEntry struct {
	name       string
	interval   time.Duration
	fn         taskFunc
	runOnStart bool
	stopCh     chan struct{}
	stopOnce   sync.Once
	updateCh   chan struct{}
	running    atomic.Bool
	failures   atomic.Uint64
	mu         sync.Mutex
	stopping   bool
	runCancel  context.CancelFunc
	wg         sync.WaitGroup
}

var (
	tasks   = make(map[string]*taskEntry)
	tasksMu sync.RWMutex
)

// Register preserves the legacy callback shape for callers without cancellable
// work. Production background tasks should use RegisterContext.
func Register(name string, interval time.Duration, runOnStart bool, fn func()) {
	RegisterContext(name, interval, runOnStart, func(context.Context) error {
		fn()
		return nil
	})
}

func RegisterContext(name string, interval time.Duration, runOnStart bool, fn taskFunc) {
	tasksMu.Lock()
	defer tasksMu.Unlock()

	if fn == nil {
		log.Warnf("task %s has nil callback, skipping", name)
		return
	}
	if _, exists := tasks[name]; exists {
		log.Warnf("task %s already registered, skipping", name)
		return
	}

	tasks[name] = &taskEntry{
		name:       name,
		interval:   interval,
		fn:         fn,
		runOnStart: runOnStart,
		stopCh:     make(chan struct{}),
		updateCh:   make(chan struct{}, 1),
	}
	if interval <= 0 {
		log.Debugf("task %s registered in disabled state", name)
		return
	}
	log.Debugf("task %s registered with interval %v, runOnStart: %v", name, interval, runOnStart)
}

func Update(name string, interval time.Duration) error {
	tasksMu.RLock()
	entry, exists := tasks[name]
	tasksMu.RUnlock()
	if !exists {
		return errors.New("task not found: " + name)
	}
	if !entry.reconfigure(interval) {
		return errors.New("task is stopping: " + name)
	}
	if interval <= 0 {
		log.Infof("task %s disabled", name)
	} else {
		log.Infof("task %s interval updated to %v", name, interval)
	}
	return nil
}

// RUN is retained for tests and legacy callers. The managed application path
// uses RuntimeWorker.Start with a cancellable root context.
func RUN() { Run(context.Background()) }

func Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	var schedulers sync.WaitGroup
	tasksMu.RLock()
	entries := make([]*taskEntry, 0, len(tasks))
	for _, entry := range tasks {
		entries = append(entries, entry)
	}
	tasksMu.RUnlock()
	for _, entry := range entries {
		schedulers.Add(1)
		go func(entry *taskEntry) {
			defer schedulers.Done()
			runTask(ctx, entry)
		}(entry)
	}
	schedulers.Wait()
}

// RuntimeWorker joins the periodic scheduler and channel maintenance queue
// under one restartable Start/Stop lifecycle.
type RuntimeWorker struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

var defaultRuntimeWorker = &RuntimeWorker{}

func DefaultRuntimeWorker() *RuntimeWorker { return defaultRuntimeWorker }

func (w *RuntimeWorker) Start(parent context.Context) error {
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
			return errors.New("scheduled task worker is still stopping")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	if err := startChannelMaintenance(ctx); err != nil {
		cancel()
		return fmt.Errorf("start channel maintenance: %w", err)
	}
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	done := w.done
	go func() {
		Run(ctx)
		close(done)
	}()
	return nil
}

func (w *RuntimeWorker) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if !w.started {
		done := w.done
		w.mu.Unlock()
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
	w.started = false
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()

	cancel()
	var result error
	if err := stopChannelMaintenance(ctx); err != nil {
		result = errors.Join(result, fmt.Errorf("stop channel maintenance: %w", err))
	}
	finished := false
	select {
	case <-done:
		finished = true
	case <-ctx.Done():
		result = errors.Join(result, ctx.Err())
	}

	w.mu.Lock()
	if finished && w.done == done {
		w.cancel = nil
		w.done = nil
	}
	w.mu.Unlock()
	return result
}

func runTask(parent context.Context, entry *taskEntry) {
	ctx, cancel := context.WithCancel(parent)
	entry.mu.Lock()
	if entry.stopping {
		entry.mu.Unlock()
		cancel()
		return
	}
	entry.runCancel = cancel
	entry.mu.Unlock()
	defer func() {
		cancel()
		entry.wg.Wait()
		entry.mu.Lock()
		entry.runCancel = nil
		entry.mu.Unlock()
	}()

	interval := entry.currentInterval()
	if entry.runOnStart && interval > 0 {
		entry.runOnceContext(ctx)
	}

	var ticker *time.Ticker
	var tickCh <-chan time.Time
	resetTicker := func(next time.Duration) {
		if ticker != nil {
			ticker.Stop()
			ticker = nil
			tickCh = nil
		}
		if next > 0 {
			ticker = time.NewTicker(next)
			tickCh = ticker.C
		}
	}
	resetTicker(interval)
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()

	for {
		select {
		case <-tickCh:
			entry.runOnceContext(ctx)
		case <-entry.updateCh:
			resetTicker(entry.currentInterval())
		case <-entry.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (entry *taskEntry) runOnce() { entry.runOnceContext(context.Background()) }

func (entry *taskEntry) runOnceContext(ctx context.Context) {
	entry.mu.Lock()
	if entry.stopping || ctx.Err() != nil {
		entry.mu.Unlock()
		return
	}
	if !entry.running.CompareAndSwap(false, true) {
		entry.mu.Unlock()
		log.Warnf("task %s still running, skipping this tick", entry.name)
		return
	}
	entry.wg.Add(1)
	entry.mu.Unlock()

	go func() {
		defer entry.wg.Done()
		defer entry.running.Store(false)
		if err := entry.fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			entry.failures.Add(1)
			log.Errorf("task %s failed: %v", entry.name, err)
		}
	}()
}

func (entry *taskEntry) stop() {
	entry.mu.Lock()
	entry.stopping = true
	cancel := entry.runCancel
	entry.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	entry.stopOnce.Do(func() { close(entry.stopCh) })
}

func (entry *taskEntry) currentInterval() time.Duration {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.interval
}

func (entry *taskEntry) reconfigure(interval time.Duration) bool {
	entry.mu.Lock()
	if entry.stopping {
		entry.mu.Unlock()
		return false
	}
	entry.interval = interval
	entry.mu.Unlock()
	select {
	case entry.updateCh <- struct{}{}:
	default:
	}
	return true
}

func waitTaskEntries(ctx context.Context, entries []*taskEntry) error {
	if len(entries) == 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		for _, entry := range entries {
			entry.wg.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
