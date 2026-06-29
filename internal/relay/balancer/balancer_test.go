package balancer

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func resetSmoothWeightedState() {
	smoothWeightedState.mu.Lock()
	defer smoothWeightedState.mu.Unlock()
	smoothWeightedState.groups = make(map[string]map[string]int)
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
