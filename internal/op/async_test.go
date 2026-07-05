package op

import "testing"

func TestAsyncWorkersLifecycleIsIdempotent(t *testing.T) {
	if err := StopAsyncWorkers(); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
	StartAsyncWorkers()
	StartAsyncWorkers()
	if err := StopAsyncWorkers(); err != nil {
		t.Fatalf("stop after start: %v", err)
	}
	if err := StopAsyncWorkers(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}
