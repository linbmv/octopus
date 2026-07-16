// Package runtime owns application background-worker lifecycles.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Worker is a managed background component. Start must return after launching
// its work; Stop must wait for that work to finish or return when ctx expires.
type Worker interface {
	Start(context.Context) error
	Stop(context.Context) error
}

// Flusher is implemented by workers with state that must be persisted after
// producers have stopped.
type Flusher interface {
	Flush(context.Context) error
}

type workerEntry struct {
	name     string
	worker   Worker
	failures atomic.Uint64
}

// Manager starts workers in registration order, stops them in reverse order,
// then flushes state while the database and other sinks are still available.
type Manager struct {
	mu       sync.Mutex
	entries  []*workerEntry
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	stopping bool
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Register(name string, worker Worker) error {
	if name == "" {
		return errors.New("runtime worker name is empty")
	}
	if worker == nil {
		return fmt.Errorf("runtime worker %q is nil", name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopping {
		return errors.New("runtime workers already started")
	}
	for _, entry := range m.entries {
		if entry.name == name {
			return fmt.Errorf("runtime worker %q already registered", name)
		}
	}
	m.entries = append(m.entries, &workerEntry{name: name, worker: worker})
	return nil
}

func (m *Manager) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	m.mu.Lock()
	if m.started || m.stopping {
		m.mu.Unlock()
		return errors.New("runtime workers already started")
	}
	ctx, cancel := context.WithCancel(parent)
	m.ctx = ctx
	m.cancel = cancel
	m.started = true
	entries := append([]*workerEntry(nil), m.entries...)
	m.mu.Unlock()

	started := make([]*workerEntry, 0, len(entries))
	for _, entry := range entries {
		if err := entry.worker.Start(ctx); err != nil {
			entry.failures.Add(1)
			cancel()
			rollbackCtx := context.Background()
			for i := len(started) - 1; i >= 0; i-- {
				if stopErr := started[i].worker.Stop(rollbackCtx); stopErr != nil {
					started[i].failures.Add(1)
				}
			}
			m.mu.Lock()
			m.started = false
			m.ctx = nil
			m.cancel = nil
			m.mu.Unlock()
			return fmt.Errorf("start runtime worker %q: %w", entry.name, err)
		}
		started = append(started, entry)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = false
	m.stopping = true
	cancel := m.cancel
	entries := append([]*workerEntry(nil), m.entries...)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var result error
	for i := len(entries) - 1; i >= 0; i-- {
		if err := entries[i].worker.Stop(ctx); err != nil {
			entries[i].failures.Add(1)
			result = errors.Join(result, fmt.Errorf("stop runtime worker %q: %w", entries[i].name, err))
		}
	}
	// Flush only after every producer has been asked to stop. Continue through
	// failures so one broken sink cannot suppress the remaining final snapshots.
	for i := len(entries) - 1; i >= 0; i-- {
		flusher, ok := entries[i].worker.(Flusher)
		if !ok {
			continue
		}
		if err := flusher.Flush(ctx); err != nil {
			entries[i].failures.Add(1)
			result = errors.Join(result, fmt.Errorf("flush runtime worker %q: %w", entries[i].name, err))
		}
	}

	m.mu.Lock()
	m.ctx = nil
	m.cancel = nil
	m.stopping = false
	m.mu.Unlock()
	return result
}
