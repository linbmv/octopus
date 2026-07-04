package health

import (
	"errors"
	"testing"
	"time"
)

func TestWeightTableRefreshKeepsPreviousOnBuildFailure(t *testing.T) {
	manager := NewHealthManager(DefaultHealthConfig())
	tables := NewWeightTableManager(manager)

	if err := tables.Refresh("g", []WeightCandidate{{ChannelID: 1, KeyID: 1, Model: "m", BaseWeight: 1}}); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	before, ok := tables.Get("g")
	if !ok {
		t.Fatal("expected initial table")
	}

	err := tables.Refresh("g", nil)
	if !errors.Is(err, ErrEmptyWeightTable) {
		t.Fatalf("Refresh(nil) error = %v, want ErrEmptyWeightTable", err)
	}
	after, ok := tables.Get("g")
	if !ok {
		t.Fatal("expected previous table to remain")
	}
	if after != before {
		t.Fatal("build failure should keep previous table atomically")
	}
}

func TestWeightTableUsesHealthScore(t *testing.T) {
	cfg := DefaultHealthConfig()
	cfg.MinSamplesForAdaptiveTimeout = 1
	manager := NewHealthManager(cfg)
	for i := 0; i < 10; i++ {
		manager.RecordTimeout(1, 1, "m", time.Second)
		manager.RecordSuccess(2, 1, "m", time.Second)
	}

	tables := NewWeightTableManager(manager)
	if err := tables.Refresh("g", []WeightCandidate{
		{ChannelID: 1, KeyID: 1, Model: "m", BaseWeight: 1},
		{ChannelID: 2, KeyID: 1, Model: "m", BaseWeight: 1},
	}); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	table, ok := tables.Get("g")
	if !ok {
		t.Fatal("expected table")
	}
	if len(table.Candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(table.Candidates))
	}
	if table.Candidates[0].Weight >= table.Candidates[1].Weight {
		t.Fatalf("unhealthy candidate weight = %f, healthy = %f", table.Candidates[0].Weight, table.Candidates[1].Weight)
	}
}

func TestWeightTableLifecycleCleanup(t *testing.T) {
	tables := NewWeightTableManager(NewHealthManager(DefaultHealthConfig()))
	if err := tables.Refresh("g", []WeightCandidate{
		{ChannelID: 1, KeyID: 1, Model: "m", BaseWeight: 1},
		{ChannelID: 2, KeyID: 2, Model: "m", BaseWeight: 1},
	}); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	tables.OnChannelDeleted(1)
	table, ok := tables.Get("g")
	if !ok || len(table.Candidates) != 1 || table.Candidates[0].ChannelID != 2 {
		t.Fatalf("channel cleanup table = %+v, ok=%v", table, ok)
	}

	tables.OnKeyDeleted(2)
	if _, ok := tables.Get("g"); ok {
		t.Fatal("expected table removed after deleting final key")
	}
}

func TestRolloutControllerPromoteAndRollback(t *testing.T) {
	controller := DefaultRolloutController()
	base := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	controller.Now = func() time.Time { return base.Add(25 * time.Hour) }

	decision := controller.Evaluate(RollbackMetrics{StageStartedAt: base, ObservedRequests: 5000})
	if decision != RolloutPromote || controller.Current().Percentage != 1 {
		t.Fatalf("decision=%s percentage=%d, want promote to 1", decision, controller.Current().Percentage)
	}

	decision = controller.Evaluate(RollbackMetrics{TimeoutRateDelta: 0.06, StageStartedAt: base, ObservedRequests: 1000})
	if decision != RolloutRollback || controller.Current().Percentage != 0 {
		t.Fatalf("decision=%s percentage=%d, want rollback to shadow", decision, controller.Current().Percentage)
	}
}
