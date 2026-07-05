package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitTaskEntriesWaitsForActiveRun(t *testing.T) {
	entry := newTestTaskEntry("wait-active")
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	entry.fn = func() {
		runs.Add(1)
		close(started)
		<-release
	}

	entry.runOnce()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- waitTaskEntries(ctx, []*taskEntry{entry})
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("wait returned before task finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not finish after task completed")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("task ran %d times, want 1", got)
	}
}

func TestWaitTaskEntriesReturnsContextError(t *testing.T) {
	entry := newTestTaskEntry("timeout")
	started := make(chan struct{})
	release := make(chan struct{})
	entry.fn = func() {
		close(started)
		<-release
	}
	defer close(release)

	entry.runOnce()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitTaskEntries(ctx, []*taskEntry{entry})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want context deadline exceeded", err)
	}
}

func TestStoppedTaskEntryDoesNotStartNewRun(t *testing.T) {
	entry := newTestTaskEntry("stopped")
	var runs atomic.Int32
	entry.fn = func() {
		runs.Add(1)
	}

	entry.stop()
	entry.runOnce()
	if got := runs.Load(); got != 0 {
		t.Fatalf("task ran %d times after stop, want 0", got)
	}
}

func TestRunReturnsAfterTasksStop(t *testing.T) {
	entry := newTestTaskEntry("run-return")
	replaceTasksForTest(t, map[string]*taskEntry{
		entry.name: entry,
	})

	done := make(chan struct{})
	go func() {
		RUN()
		close(done)
	}()

	entry.stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RUN did not return after task stopped")
	}
}

func newTestTaskEntry(name string) *taskEntry {
	return &taskEntry{
		name:     name,
		interval: time.Hour,
		stopCh:   make(chan struct{}),
		updateCh: make(chan time.Duration),
	}
}

func replaceTasksForTest(t *testing.T, next map[string]*taskEntry) {
	t.Helper()
	tasksMu.Lock()
	old := tasks
	tasks = next
	tasksMu.Unlock()

	t.Cleanup(func() {
		tasksMu.Lock()
		entries := make([]*taskEntry, 0, len(tasks))
		for _, entry := range tasks {
			entries = append(entries, entry)
		}
		tasks = old
		tasksMu.Unlock()
		for _, entry := range entries {
			entry.stop()
		}
	})
}
