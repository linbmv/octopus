package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestWaitTaskEntriesWaitsForActiveRun(t *testing.T) {
	entry := newTestTaskEntry("wait-active")
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	entry.fn = func(context.Context) error {
		runs.Add(1)
		close(started)
		<-release
		return nil
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
	entry.fn = func(context.Context) error {
		close(started)
		<-release
		return nil
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
	entry.fn = func(context.Context) error {
		runs.Add(1)
		return nil
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

func TestInitRegistersAllTasksWhenOneSettingLookupFails(t *testing.T) {
	for _, failedKey := range []model.SettingKey{
		model.SettingKeyModelInfoUpdateInterval,
		model.SettingKeySyncLLMInterval,
		model.SettingKeyStatsSaveInterval,
	} {
		t.Run(string(failedKey), func(t *testing.T) {
			replaceTasksForTest(t, make(map[string]*taskEntry))
			initWithSettingGetter(func(key model.SettingKey) (int, error) {
				if key == failedKey {
					return 0, errors.New("corrupt interval")
				}
				switch key {
				case model.SettingKeyModelInfoUpdateInterval:
					return 1, nil
				case model.SettingKeySyncLLMInterval:
					return 2, nil
				case model.SettingKeyStatsSaveInterval:
					return 3, nil
				default:
					return 0, errors.New("unexpected key")
				}
			})

			wantIntervals := map[string]time.Duration{
				string(model.SettingKeyModelInfoUpdateInterval): time.Hour,
				TaskBaseUrlDelay:                        time.Hour,
				string(model.SettingKeySyncLLMInterval): 2 * time.Hour,
				TaskStatsSave:                           3 * time.Minute,
				TaskRelayLogSave:                        10 * time.Minute,
			}
			switch failedKey {
			case model.SettingKeyModelInfoUpdateInterval:
				wantIntervals[string(failedKey)] = 24 * time.Hour
			case model.SettingKeySyncLLMInterval:
				wantIntervals[string(failedKey)] = 24 * time.Hour
			case model.SettingKeyStatsSaveInterval:
				wantIntervals[TaskStatsSave] = 10 * time.Minute
			}

			for name, want := range wantIntervals {
				entry := taskEntryForTest(t, name)
				if got := entry.currentInterval(); got != want {
					t.Fatalf("task %s interval = %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestReconfigureSettingUpdatesStatsSaveInterval(t *testing.T) {
	replaceTasksForTest(t, make(map[string]*taskEntry))
	Register(TaskStatsSave, 10*time.Minute, false, func() {})

	if err := ReconfigureSetting(model.SettingKeyStatsSaveInterval, "7"); err != nil {
		t.Fatalf("ReconfigureSetting returned error: %v", err)
	}
	if got := taskEntryForTest(t, TaskStatsSave).currentInterval(); got != 7*time.Minute {
		t.Fatalf("stats task interval = %v, want 7m", got)
	}
}

func TestTaskCanBeReenabledAfterZeroInterval(t *testing.T) {
	replaceTasksForTest(t, make(map[string]*taskEntry))
	runs := make(chan struct{}, 1)
	Register("reenable", time.Hour, false, func() {
		select {
		case runs <- struct{}{}:
		default:
		}
	})
	entry := taskEntryForTest(t, "reenable")

	runDone := make(chan struct{})
	go func() {
		RUN()
		close(runDone)
	}()

	if err := Update("reenable", 0); err != nil {
		t.Fatalf("disable task: %v", err)
	}
	if got := entry.currentInterval(); got != 0 {
		t.Fatalf("disabled interval = %v, want 0", got)
	}
	// The definition must remain addressable while disabled.
	if taskEntryForTest(t, "reenable") != entry {
		t.Fatal("disabled task definition was replaced")
	}

	if err := Update("reenable", 5*time.Millisecond); err != nil {
		t.Fatalf("re-enable task: %v", err)
	}
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("task did not run after re-enable")
	}

	entry.stop()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("RUN did not return after re-enabled task stopped")
	}
}

func TestRuntimeWorkerCancelsActiveScheduledTask(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	entry := newTestTaskEntry("context-cancel")
	entry.runOnStart = true
	entry.fn = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}
	replaceTasksForTest(t, map[string]*taskEntry{entry.name: entry})

	oldQueue := channelMaintenanceQueue
	channelMaintenanceQueue = mustChannelMaintenanceQueue()
	t.Cleanup(func() { channelMaintenanceQueue = oldQueue })
	worker := &RuntimeWorker{}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduled task did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active scheduled task did not observe cancellation")
	}
}

func newTestTaskEntry(name string) *taskEntry {
	return &taskEntry{
		name:     name,
		interval: time.Hour,
		fn:       func(context.Context) error { return nil },
		stopCh:   make(chan struct{}),
		updateCh: make(chan struct{}, 1),
	}
}

func taskEntryForTest(t *testing.T, name string) *taskEntry {
	t.Helper()
	tasksMu.RLock()
	entry, ok := tasks[name]
	tasksMu.RUnlock()
	if !ok {
		t.Fatalf("task %s not registered", name)
	}
	return entry
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
