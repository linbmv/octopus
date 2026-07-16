package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type SubmitResult string

const (
	SubmitAccepted  SubmitResult = "accepted"
	SubmitCoalesced SubmitResult = "coalesced"
	SubmitDropped   SubmitResult = "dropped"
	SubmitStopped   SubmitResult = "stopped"
)

type QueueStats struct {
	Accepted    uint64 `json:"accepted"`
	Coalesced   uint64 `json:"coalesced"`
	Dropped     uint64 `json:"dropped"`
	Failures    uint64 `json:"failures"`
	QueueDepth  int    `json:"queue_depth"`
	QueueLimit  int    `json:"queue_limit"`
	Concurrency int    `json:"concurrency"`
}

type JobQueueConfig[J any] struct {
	Name        string
	QueueDepth  int
	Concurrency int
	Key         func(J) string
	Handle      func(context.Context, J) error
	OnError     func(error)
}

// JobQueue is a bounded, restartable worker pool. Optional keys coalesce jobs
// already queued or running; a full queue drops the new job explicitly.
type JobQueue[J any] struct {
	config JobQueueConfig[J]

	mu      sync.Mutex
	queue   chan J
	pending map[string]struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	accepted  atomic.Uint64
	coalesced atomic.Uint64
	dropped   atomic.Uint64
	failures  atomic.Uint64
}

func NewJobQueue[J any](config JobQueueConfig[J]) (*JobQueue[J], error) {
	if config.Name == "" {
		return nil, errors.New("job queue name is empty")
	}
	if config.QueueDepth <= 0 {
		return nil, errors.New("job queue depth must be positive")
	}
	if config.Concurrency <= 0 {
		return nil, errors.New("job queue concurrency must be positive")
	}
	if config.Handle == nil {
		return nil, errors.New("job queue handler is nil")
	}
	return &JobQueue[J]{config: config}, nil
}

func (q *JobQueue[J]) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		return nil
	}
	if q.done != nil {
		select {
		case <-q.done:
			q.clearStoppedLocked()
		default:
			return errors.New("job queue is still stopping")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	q.queue = make(chan J, q.config.QueueDepth)
	q.pending = make(map[string]struct{})
	q.cancel = cancel
	q.done = make(chan struct{})
	q.started = true

	var workers sync.WaitGroup
	workers.Add(q.config.Concurrency)
	for i := 0; i < q.config.Concurrency; i++ {
		go func() {
			defer workers.Done()
			q.run(ctx)
		}()
	}
	done := q.done
	go func() {
		workers.Wait()
		close(done)
	}()
	return nil
}

func (q *JobQueue[J]) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if !q.started {
		done := q.done
		q.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
			q.mu.Lock()
			if q.done == done {
				q.clearStoppedLocked()
			}
			q.mu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	q.started = false
	cancel := q.cancel
	done := q.done
	q.mu.Unlock()

	cancel()
	select {
	case <-done:
		q.mu.Lock()
		if q.done == done {
			q.clearStoppedLocked()
		}
		q.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *JobQueue[J]) clearStoppedLocked() {
	q.queue = nil
	q.pending = nil
	q.cancel = nil
	q.done = nil
}

func (q *JobQueue[J]) Submit(job J) SubmitResult {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started || q.queue == nil {
		q.dropped.Add(1)
		return SubmitStopped
	}
	key := ""
	if q.config.Key != nil {
		key = q.config.Key(job)
		if key != "" {
			if _, exists := q.pending[key]; exists {
				q.coalesced.Add(1)
				return SubmitCoalesced
			}
			q.pending[key] = struct{}{}
		}
	}
	select {
	case q.queue <- job:
		q.accepted.Add(1)
		return SubmitAccepted
	default:
		if key != "" {
			delete(q.pending, key)
		}
		q.dropped.Add(1)
		return SubmitDropped
	}
}

func (q *JobQueue[J]) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-q.queue:
			err := q.config.Handle(ctx, job)
			if err != nil {
				q.failures.Add(1)
				if q.config.OnError != nil {
					q.config.OnError(err)
				}
			}
			if q.config.Key != nil {
				if key := q.config.Key(job); key != "" {
					q.mu.Lock()
					delete(q.pending, key)
					q.mu.Unlock()
				}
			}
		}
	}
}

func (q *JobQueue[J]) Stats() QueueStats {
	q.mu.Lock()
	depth := 0
	if q.queue != nil {
		depth = len(q.queue)
	}
	q.mu.Unlock()
	return QueueStats{
		Accepted:    q.accepted.Load(),
		Coalesced:   q.coalesced.Load(),
		Dropped:     q.dropped.Load(),
		Failures:    q.failures.Load(),
		QueueDepth:  depth,
		QueueLimit:  q.config.QueueDepth,
		Concurrency: q.config.Concurrency,
	}
}
