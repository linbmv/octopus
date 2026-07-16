package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSyncModelsStartsWithNoSuccessfulRun(t *testing.T) {
	syncModelsStateMu.Lock()
	old := lastSyncModelsTime
	lastSyncModelsTime = time.Time{}
	syncModelsStateMu.Unlock()
	t.Cleanup(func() {
		syncModelsStateMu.Lock()
		lastSyncModelsTime = old
		syncModelsStateMu.Unlock()
	})

	if got := GetLastSyncModelsTime(); !got.IsZero() {
		t.Fatalf("initial last sync = %v, want zero", got)
	}
	if err := SyncModelsNow(context.Background()); err != nil {
		t.Fatalf("SyncModelsNow() error = %v", err)
	}
	if got := GetLastSyncModelsTime(); got.IsZero() {
		t.Fatal("successful synchronization did not record completion time")
	}
}

func TestSyncModelsRejectsOverlappingRun(t *testing.T) {
	syncModelsMu.Lock()
	defer syncModelsMu.Unlock()
	if err := SyncModelsNow(context.Background()); !errors.Is(err, ErrSyncModelsInProgress) {
		t.Fatalf("SyncModelsNow() error = %v, want ErrSyncModelsInProgress", err)
	}
}
