package balancer

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func resetSmoothWeightedState() {
	smoothWeightedState.clear()
	SetHealthWeightFunc(nil)
}

func TestWeightedCandidatesUsesSmoothWeightedRoundRobin(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()

	items := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a", Weight: 5},
		{ID: 2, ChannelID: 2, ModelName: "b", Weight: 1},
	}
	balancer := &Weighted{}

	counts := map[int]int{}
	for range 6 {
		candidates := balancer.Candidates(items)
		if len(candidates) != len(items) {
			t.Fatalf("candidate count = %d, want %d", len(candidates), len(items))
		}
		counts[candidates[0].ChannelID]++
	}

	if counts[1] != 5 || counts[2] != 1 {
		t.Fatalf("selection counts = %#v, want channel 1 five times and channel 2 once", counts)
	}
}

func TestWeightedCandidatesAppliesHealthWeight(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()

	items := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a", Weight: 10},
		{ID: 2, ChannelID: 2, ModelName: "b", Weight: 10},
	}
	SetHealthWeightFunc(func(item model.GroupItem) float64 {
		if item.ChannelID == 1 {
			return 0.1
		}
		return 1
	})

	balancer := &Weighted{}
	counts := map[int]int{}
	for range 11 {
		candidates := balancer.Candidates(items)
		counts[candidates[0].ChannelID]++
	}

	if counts[1] != 1 || counts[2] != 10 {
		t.Fatalf("selection counts = %#v, want health-adjusted 1:10 distribution", counts)
	}
}

func TestWeightedCandidatesDoesNotApplyHealthWithoutCallback(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()

	items := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a", Weight: 1},
		{ID: 2, ChannelID: 2, ModelName: "b", Weight: 1},
	}
	counts := map[int]int{}
	for range 4 {
		counts[(&Weighted{}).Candidates(items)[0].ChannelID]++
	}
	if counts[1] != 2 || counts[2] != 2 {
		t.Fatalf("selection counts = %#v, want unchanged equal weights", counts)
	}
}

func TestFailoverHealthOnlyReordersSamePriority(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()
	SetHealthWeightFunc(func(item model.GroupItem) float64 {
		return map[int]float64{1: 0.1, 2: 0.2, 3: 0.9}[item.ChannelID]
	})

	items := []model.GroupItem{
		{ID: 1, ChannelID: 1, Priority: 0},
		{ID: 2, ChannelID: 2, Priority: 1},
		{ID: 3, ChannelID: 3, Priority: 1},
	}
	got := (&Failover{}).Candidates(items)
	if got[0].ChannelID != 1 {
		t.Fatalf("first channel = %d, health must not override failover priority", got[0].ChannelID)
	}
	if got[1].ChannelID != 3 || got[2].ChannelID != 2 {
		t.Fatalf("same-priority order = [%d %d], want healthier channel first", got[1].ChannelID, got[2].ChannelID)
	}
}

func TestFailoverKeepsStableOrderWithoutHealthCallback(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()
	items := []model.GroupItem{
		{ID: 2, ChannelID: 2, Priority: 1},
		{ID: 1, ChannelID: 1, Priority: 1},
	}
	got := (&Failover{}).Candidates(items)
	if got[0].ChannelID != 2 || got[1].ChannelID != 1 {
		t.Fatalf("order = [%d %d], want stable input order while health is disabled", got[0].ChannelID, got[1].ChannelID)
	}
}

func TestWeightedCandidatesTreatsNonPositiveWeightAsOne(t *testing.T) {
	resetSmoothWeightedState()
	defer resetSmoothWeightedState()

	items := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "a", Weight: 0},
		{ID: 2, ChannelID: 2, ModelName: "b", Weight: -3},
	}
	balancer := &Weighted{}

	counts := map[int]int{}
	for range 4 {
		candidates := balancer.Candidates(items)
		counts[candidates[0].ChannelID]++
	}

	if counts[1] != 2 || counts[2] != 2 {
		t.Fatalf("selection counts = %#v, want even distribution", counts)
	}
}
