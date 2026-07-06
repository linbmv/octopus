package op

import (
	"context"
	"sync"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestStatsServiceMetricUpdates(t *testing.T) {
	service := NewStatsService()
	metrics := model.StatsMetrics{
		RequestSuccess: 2,
		InputToken:     10,
	}

	if err := service.TotalUpdate(metrics); err != nil {
		t.Fatalf("total update: %v", err)
	}
	if got := service.TotalGet(); got.ID != 1 || got.RequestSuccess != 2 || got.InputToken != 10 {
		t.Fatalf("unexpected total stats: %#v", got)
	}

	if err := service.ChannelUpdate(7, metrics); err != nil {
		t.Fatalf("channel update: %v", err)
	}
	if got := service.ChannelGet(7); got.ChannelID != 7 || got.RequestSuccess != 2 {
		t.Fatalf("unexpected channel stats: %#v", got)
	}
	if got := service.takeDirtyChannels(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("unexpected dirty channels: %#v", got)
	}
	if got := service.takeDirtyChannels(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("dirty snapshot should not clear before db save succeeds: %#v", got)
	}
	snap := service.dirtyChannels.snapshot()
	service.dirtyChannels.clearUnchanged(snap)
	if got := service.takeDirtyChannels(); len(got) != 0 {
		t.Fatalf("expected dirty channels to clear after ack, got %#v", got)
	}
}

func TestStatsServiceChannelUpdateConcurrent(t *testing.T) {
	service := NewStatsService()
	const goroutines = 100
	const updatesPerGoroutine = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				if err := service.ChannelUpdate(7, model.StatsMetrics{InputToken: 1, RequestSuccess: 1}); err != nil {
					t.Errorf("channel update: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	got := service.ChannelGet(7)
	want := int64(goroutines * updatesPerGoroutine)
	if got.InputToken != want || got.RequestSuccess != want {
		t.Fatalf("concurrent channel updates lost metrics: got=%#v want=%d", got, want)
	}
}

func TestDirtySetClearUnchangedPreservesNewerMarks(t *testing.T) {
	dirty := newDirtySet()
	dirty.mark(7)
	snap := dirty.snapshot()
	dirty.mark(7)
	dirty.clearUnchanged(snap)

	if got := dirtyIDs(dirty.snapshot()); len(got) != 1 || got[0] != 7 {
		t.Fatalf("newer dirty mark should survive stale ack: %#v", got)
	}
}

func TestStatsServiceAPIKeyGetMarksDirty(t *testing.T) {
	service := NewStatsService()

	got := service.APIKeyGet(9)
	if got.APIKeyID != 9 {
		t.Fatalf("unexpected api key stats: %#v", got)
	}
	if dirty := service.takeDirtyAPIKeys(); len(dirty) != 1 || dirty[0] != 9 {
		t.Fatalf("unexpected dirty api keys: %#v", dirty)
	}
}

func TestStatsServiceDailyUpdateSignalsPreviousDay(t *testing.T) {
	service := NewStatsService()
	service.daily = model.StatsDaily{
		Date: "20000101",
		StatsMetrics: model.StatsMetrics{
			RequestSuccess: 3,
		},
	}

	if err := service.DailyUpdate(context.Background(), model.StatsMetrics{RequestSuccess: 1}); err != nil {
		t.Fatalf("daily update: %v", err)
	}

	select {
	case <-service.pendingDailyNotify:
	default:
		t.Fatal("expected previous daily snapshot notification")
	}

	pending := service.drainPendingDailySnapshots()
	if len(pending) != 1 || pending[0].Date != "20000101" || pending[0].RequestSuccess != 3 {
		t.Fatalf("unexpected pending daily snapshot: %#v", pending)
	}

	if got := service.TodayGet(); got.Date == "20000101" || got.RequestSuccess != 1 {
		t.Fatalf("unexpected new daily snapshot: %#v", got)
	}
}

func TestStatsServiceDrainPendingDailySnapshots(t *testing.T) {
	service := NewStatsService()
	service.signalSave(model.StatsDaily{
		Date:         "20000101",
		StatsMetrics: model.StatsMetrics{RequestSuccess: 2},
	})
	service.signalSave(model.StatsDaily{})
	service.signalSave(model.StatsDaily{
		Date:         "20000102",
		StatsMetrics: model.StatsMetrics{RequestSuccess: 3},
	})

	got := service.drainPendingDailySnapshots()
	if len(got) != 2 {
		t.Fatalf("drained %d snapshots, want 2: %#v", len(got), got)
	}
	if got[0].Date != "20000101" || got[0].RequestSuccess != 2 {
		t.Fatalf("unexpected first snapshot: %#v", got[0])
	}
	if got[1].Date != "20000102" || got[1].RequestSuccess != 3 {
		t.Fatalf("unexpected second snapshot: %#v", got[1])
	}

	again := service.drainPendingDailySnapshots()
	if len(again) != 2 {
		t.Fatalf("drain should not ack snapshots before db save succeeds: %#v", again)
	}
	for _, daily := range got {
		service.ackPendingDaily(daily)
	}
	if remaining := service.drainPendingDailySnapshots(); len(remaining) != 0 {
		t.Fatalf("expected snapshots to clear after ack, got %#v", remaining)
	}

	select {
	case <-service.pendingDailyNotify:
	default:
	}
}
