package op

import (
	"context"
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
	if got := service.takeDirtyChannels(); len(got) != 0 {
		t.Fatalf("expected dirty channels to reset, got %#v", got)
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
	case prev := <-service.saveSignal:
		if prev.Date != "20000101" || prev.RequestSuccess != 3 {
			t.Fatalf("unexpected previous daily snapshot: %#v", prev)
		}
	default:
		t.Fatal("expected previous daily snapshot to be queued")
	}

	if got := service.TodayGet(); got.Date == "20000101" || got.RequestSuccess != 1 {
		t.Fatalf("unexpected new daily snapshot: %#v", got)
	}
}

func TestStatsServiceDrainPendingDailySnapshots(t *testing.T) {
	service := NewStatsService()
	service.saveSignal <- model.StatsDaily{
		Date:         "20000101",
		StatsMetrics: model.StatsMetrics{RequestSuccess: 2},
	}
	service.saveSignal <- model.StatsDaily{}
	service.saveSignal <- model.StatsDaily{
		Date:         "20000102",
		StatsMetrics: model.StatsMetrics{RequestSuccess: 3},
	}

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

	select {
	case leftover := <-service.saveSignal:
		t.Fatalf("expected queue to be drained, got %#v", leftover)
	default:
	}
}
