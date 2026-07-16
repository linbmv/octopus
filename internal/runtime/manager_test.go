package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type recordingWorker struct {
	name   string
	events *[]string
	mu     *sync.Mutex
	flush  bool
}

func (w recordingWorker) record(event string) {
	w.mu.Lock()
	*w.events = append(*w.events, event+":"+w.name)
	w.mu.Unlock()
}
func (w recordingWorker) Start(context.Context) error { w.record("start"); return nil }
func (w recordingWorker) Stop(context.Context) error  { w.record("stop"); return nil }
func (w recordingWorker) Flush(context.Context) error {
	if w.flush {
		w.record("flush")
	}
	return nil
}

func TestManagerLifecycleOrder(t *testing.T) {
	manager := NewManager()
	var events []string
	var mu sync.Mutex
	for _, name := range []string{"a", "b"} {
		if err := manager.Register(name, recordingWorker{name: name, events: &events, mu: &mu, flush: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:a", "start:b", "stop:b", "stop:a", "flush:b", "flush:a"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestJobQueueBoundsDeduplicatesAndCancels(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	queue, err := NewJobQueue(JobQueueConfig[int]{
		Name: "test", QueueDepth: 1, Concurrency: 1,
		Key: func(v int) string { return string(rune(v)) },
		Handle: func(ctx context.Context, value int) error {
			if value == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := queue.Submit(1); got != SubmitAccepted {
		t.Fatalf("first submit = %s", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if got := queue.Submit(1); got != SubmitCoalesced {
		t.Fatalf("duplicate submit = %s", got)
	}
	if got := queue.Submit(2); got != SubmitAccepted {
		t.Fatalf("queued submit = %s", got)
	}
	if got := queue.Submit(3); got != SubmitDropped {
		t.Fatalf("overflow submit = %s", got)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := queue.Stop(stopCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stats := queue.Stats()
	if stats.Accepted != 2 || stats.Coalesced != 1 || stats.Dropped != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	close(release)
}

func TestJobQueueDoesNotRestartWhileTimedOutWorkerIsStillRunning(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	queue, err := NewJobQueue(JobQueueConfig[int]{
		Name: "slow", QueueDepth: 1, Concurrency: 1,
		Handle: func(context.Context, int) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.Submit(1) != SubmitAccepted {
		t.Fatal("job not accepted")
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = queue.Stop(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want deadline", err)
	}
	if err := queue.Start(context.Background()); err == nil {
		t.Fatal("queue restarted while previous worker was still running")
	}
	close(release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	if err := queue.Stop(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("queue did not restart after prior worker completed: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	if err := queue.Stop(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
}
