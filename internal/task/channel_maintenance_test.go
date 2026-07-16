package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestSubmitChannelMaintenanceDeduplicatesPendingChannel(t *testing.T) {
	oldRunner := channelMaintenanceRunner
	oldQueue := channelMaintenanceQueue
	channelMaintenanceQueue = mustChannelMaintenanceQueue()
	ctx, cancel := context.WithCancel(context.Background())
	if err := startChannelMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	runDone := make(chan struct{}, 2)
	var runs atomic.Int32

	channelMaintenanceRunner = func(ctx context.Context, channel model.Channel) error {
		if runs.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			runDone <- struct{}{}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = stopChannelMaintenance(stopCtx)
		channelMaintenanceRunner = oldRunner
		channelMaintenanceQueue = oldQueue
	})

	if !SubmitChannelMaintenance(model.Channel{ID: 12345, Name: "dedupe"}) {
		t.Fatal("first submit should be accepted")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("maintenance job did not start")
	}

	if !SubmitChannelMaintenance(model.Channel{ID: 12345, Name: "dedupe"}) {
		t.Fatal("duplicate submit should be treated as already pending")
	}

	close(release)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("maintenance job did not finish")
	}
	time.Sleep(20 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runner called %d times, want 1", got)
	}
	stats := ChannelMaintenanceQueueStats()
	if stats.Accepted != 1 || stats.Coalesced != 1 {
		t.Fatalf("queue stats = %+v", stats)
	}
}

func TestChannelMaintenanceStopCancelsActiveJobAndCanRestart(t *testing.T) {
	oldRunner := channelMaintenanceRunner
	oldQueue := channelMaintenanceQueue
	channelMaintenanceQueue = mustChannelMaintenanceQueue()
	started := make(chan struct{}, 2)
	channelMaintenanceRunner = func(ctx context.Context, channel model.Channel) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() {
		channelMaintenanceRunner = oldRunner
		channelMaintenanceQueue = oldQueue
	})

	for run := 0; run < 2; run++ {
		parent, cancel := context.WithCancel(context.Background())
		if err := startChannelMaintenance(parent); err != nil {
			t.Fatal(err)
		}
		if !SubmitChannelMaintenance(model.Channel{ID: run + 1}) {
			t.Fatal("submit rejected")
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("job did not start")
		}
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		if err := stopChannelMaintenance(stopCtx); err != nil {
			stopCancel()
			t.Fatal(err)
		}
		stopCancel()
	}
}

func TestCloneChannelForMaintenanceDecouplesMutableFields(t *testing.T) {
	stats := &model.StatsChannel{ChannelID: 1}
	rewriteValue := `{"enabled":true}`
	original := model.Channel{
		ID:               1,
		BaseUrls:         []model.BaseUrl{{URL: "https://example.com", Delay: 10}},
		Keys:             []model.ChannelKey{{ID: 1, ChannelID: 1, ChannelKey: "key"}},
		CustomHeader:     []model.CustomHeader{{HeaderKey: "X-Test", HeaderValue: "a"}},
		HeaderRules:      []model.HeaderRule{{Action: "set", HeaderKey: "X-Rule", HeaderValue: "a"}},
		JSONRewriteRules: []model.JSONRewriteRule{{Action: "override", Path: "/enabled", Value: &rewriteValue}},
		Stats:            stats,
	}

	cloned := cloneChannelForMaintenance(original)
	cloned.BaseUrls[0].Delay = 20
	cloned.Keys[0].ChannelKey = "changed"
	cloned.CustomHeader[0].HeaderValue = "b"
	cloned.HeaderRules[0].HeaderValue = "b"
	*cloned.JSONRewriteRules[0].Value = `false`

	if original.BaseUrls[0].Delay != 10 {
		t.Fatalf("base urls share backing array")
	}
	if original.Keys[0].ChannelKey != "key" {
		t.Fatalf("keys share backing array")
	}
	if original.CustomHeader[0].HeaderValue != "a" {
		t.Fatalf("custom headers share backing array")
	}
	if original.HeaderRules[0].HeaderValue != "a" {
		t.Fatalf("header rules share backing array")
	}
	if *original.JSONRewriteRules[0].Value != `{"enabled":true}` {
		t.Fatalf("JSON rewrite values share pointers")
	}
	if cloned.Stats != nil {
		t.Fatalf("cloned stats should be nil")
	}
}
