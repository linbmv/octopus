package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
)

type taskEntry struct {
	name       string
	interval   time.Duration
	fn         func()
	runOnStart bool
	ticker     *time.Ticker
	stopCh     chan struct{}
	stopOnce   sync.Once
	updateCh   chan time.Duration
	running    atomic.Bool
	mu         sync.Mutex
	stopping   bool
	wg         sync.WaitGroup
}

var (
	tasks   = make(map[string]*taskEntry)
	tasksMu sync.RWMutex
)

// Register 注册一个定时任务
// runOnStart: 是否在启动时立即执行一次
func Register(name string, interval time.Duration, runOnStart bool, fn func()) {
	if interval <= 0 {
		log.Debugf("task %s not registered: interval is 0", name)
		return
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

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
		updateCh:   make(chan time.Duration),
	}
	log.Debugf("task %s registered with interval %v, runOnStart: %v", name, interval, runOnStart)
}

// Update 更新任务的执行间隔
// 当 interval 为 0 时，删除任务
func Update(name string, interval time.Duration) {
	tasksMu.Lock()
	entry, exists := tasks[name]
	if !exists {
		tasksMu.Unlock()
		log.Warnf("task %s not found", name)
		return
	}

	if interval <= 0 {
		delete(tasks, name)
		tasksMu.Unlock()
		entry.stop()
		log.Infof("task %s removed: interval is 0", name)
		return
	}
	tasksMu.Unlock()

	select {
	case entry.updateCh <- interval:
		log.Infof("task %s interval updated to %v", name, interval)
	default:
		log.Warnf("task %s update channel full, skipping", name)
	}
}

// RUN 启动所有注册的任务
func RUN() {
	var wg sync.WaitGroup
	tasksMu.RLock()
	for _, entry := range tasks {
		wg.Add(1)
		go func(entry *taskEntry) {
			defer wg.Done()
			runTask(entry)
		}(entry)
	}
	tasksMu.RUnlock()
	wg.Wait()
}

func Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tasksMu.Lock()
	entries := make([]*taskEntry, 0, len(tasks))
	for name, entry := range tasks {
		entries = append(entries, entry)
		delete(tasks, name)
	}
	tasksMu.Unlock()

	for _, entry := range entries {
		entry.stop()
	}

	var closeErr error
	if err := waitTaskEntries(ctx, entries); err != nil {
		closeErr = err
	}
	if err := stopChannelMaintenance(ctx); err != nil && !errors.Is(err, context.Canceled) && closeErr == nil {
		return err
	}
	return closeErr
}

func runTask(entry *taskEntry) {
	// 根据配置决定是否在启动时立即执行
	if entry.runOnStart {
		entry.runOnce()
	}

	entry.ticker = time.NewTicker(entry.interval)
	defer entry.ticker.Stop()

	for {
		select {
		case <-entry.ticker.C:
			entry.runOnce()
		case newInterval := <-entry.updateCh:
			entry.ticker.Stop()
			entry.interval = newInterval
			entry.ticker = time.NewTicker(newInterval)
		case <-entry.stopCh:
			return
		}
	}
}

func (entry *taskEntry) runOnce() {
	entry.mu.Lock()
	if entry.stopping {
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
		entry.fn()
	}()
}

func (entry *taskEntry) stop() {
	entry.mu.Lock()
	entry.stopping = true
	entry.mu.Unlock()
	entry.stopOnce.Do(func() {
		close(entry.stopCh)
	})
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
