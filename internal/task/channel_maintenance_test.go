package task

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestSubmitChannelMaintenanceDeduplicatesPendingChannel(t *testing.T) {
	oldRunner := channelMaintenanceRunner
	started := make(chan struct{})
	release := make(chan struct{})
	runDone := make(chan struct{}, 2)
	var runs atomic.Int32

	channelMaintenanceRunner = func(channel model.Channel) {
		if runs.Add(1) == 1 {
			close(started)
		}
		<-release
		runDone <- struct{}{}
	}
	t.Cleanup(func() {
		channelMaintenanceRunner = oldRunner
		channelMaintenanceMu.Lock()
		delete(channelMaintenancePending, 12345)
		channelMaintenanceMu.Unlock()
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
}

func TestCloneChannelForMaintenanceDecouplesMutableFields(t *testing.T) {
	stats := &model.StatsChannel{ChannelID: 1}
	original := model.Channel{
		ID:           1,
		BaseUrls:     []model.BaseUrl{{URL: "https://example.com", Delay: 10}},
		Keys:         []model.ChannelKey{{ID: 1, ChannelID: 1, ChannelKey: "key"}},
		CustomHeader: []model.CustomHeader{{HeaderKey: "X-Test", HeaderValue: "a"}},
		Stats:        stats,
	}

	cloned := cloneChannelForMaintenance(original)
	cloned.BaseUrls[0].Delay = 20
	cloned.Keys[0].ChannelKey = "changed"
	cloned.CustomHeader[0].HeaderValue = "b"

	if original.BaseUrls[0].Delay != 10 {
		t.Fatalf("base urls share backing array")
	}
	if original.Keys[0].ChannelKey != "key" {
		t.Fatalf("keys share backing array")
	}
	if original.CustomHeader[0].HeaderValue != "a" {
		t.Fatalf("custom headers share backing array")
	}
	if cloned.Stats != nil {
		t.Fatalf("cloned stats should be nil")
	}
}
