package shutdown

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type testLogger struct {
	mu      sync.Mutex
	entries []string
	started chan struct{}
	once    sync.Once
}

func (l *testLogger) Infof(template string, args ...interface{}) {
	l.record(template)
	if strings.Contains(template, "Program started") {
		l.once.Do(func() { close(l.started) })
	}
}

func (l *testLogger) Errorf(template string, args ...interface{}) { l.record(template) }
func (l *testLogger) Warnf(template string, args ...interface{})  { l.record(template) }
func (l *testLogger) Debugf(template string, args ...interface{}) { l.record(template) }

func (l *testLogger) record(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *testLogger) contains(text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if strings.Contains(entry, text) {
			return true
		}
	}
	return false
}

func TestShutdownRunsFunctionsInReverseOrderAndLogsErrors(t *testing.T) {
	logger := &testLogger{started: make(chan struct{})}
	Init(logger)
	var order []int
	Register(func() error {
		order = append(order, 1)
		return nil
	})
	Register(func() error {
		order = append(order, 2)
		return errors.New("close failed")
	})

	Shutdown()

	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("shutdown order = %v, want [2 1]", order)
	}
	if !logger.contains("Closing functions execution failed") {
		t.Fatal("Shutdown() did not log the close error")
	}
	if !logger.contains("Shutdown completed successfully") {
		t.Fatal("Shutdown() did not log completion")
	}
}

func TestListenReturnsOnSignalWhenNothingRegistered(t *testing.T) {
	logger := &testLogger{started: make(chan struct{})}
	Init(logger)
	done := make(chan struct{})
	go func() {
		Listen()
		close(done)
	}()

	select {
	case <-logger.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen() did not start")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen() did not return after SIGHUP")
	}
	if !logger.contains("Received exit signal") {
		t.Fatal("Listen() did not log the received signal")
	}
}
