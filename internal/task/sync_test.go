package task

import (
	"context"
	"errors"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

// fakeChannelGetter records how many times each channel is looked up so the
// test can assert that a (channelID, modelName) pair is only turned into a
// probe job once, regardless of how many groups/items reference it.
func fakeChannelGetter(calls map[int]int, failIDs map[int]error) func(int) (*model.Channel, error) {
	return func(id int) (*model.Channel, error) {
		calls[id]++
		if err, ok := failIDs[id]; ok {
			return nil, err
		}
		return &model.Channel{ID: id}, nil
	}
}

func TestCollectCompactProbeJobsDeduplicatesAcrossGroups(t *testing.T) {
	groups := []compactProbeGroup{
		{
			ID:   1,
			Name: "gpt-5.5",
			Items: []model.GroupItem{
				{ID: 11, GroupID: 1, ChannelID: 21, ModelName: "gpt-5.5"},
				{ID: 12, GroupID: 1, ChannelID: 22, ModelName: "gpt-5.5"},
			},
		},
		{
			ID:   2,
			Name: "gpt-5.5-mirror",
			Items: []model.GroupItem{
				// Same (channel 21, gpt-5.5) as group 1 item 11 -> must dedupe.
				{ID: 13, GroupID: 2, ChannelID: 21, ModelName: "gpt-5.5"},
				// Same channel 22 but a different model -> distinct job.
				{ID: 14, GroupID: 2, ChannelID: 22, ModelName: "gpt-5.5-mini"},
			},
		},
	}

	calls := map[int]int{}
	jobs, setupErrors, canceled := collectCompactProbeJobs(context.Background(), groups, fakeChannelGetter(calls, nil))

	if canceled {
		t.Fatal("collectCompactProbeJobs reported canceled for a live context")
	}
	if len(setupErrors) != 0 {
		t.Fatalf("expected no setup errors, got %v", setupErrors)
	}

	// Distinct probe keys: (21,gpt-5.5), (22,gpt-5.5), (22,gpt-5.5-mini) = 3.
	if len(jobs) != 3 {
		t.Fatalf("expected 3 deduped jobs, got %d: %+v", len(jobs), jobs)
	}

	seen := map[compactProbeKey]int{}
	for _, job := range jobs {
		seen[job.key]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("probe key %+v appeared %d times, want exactly 1", key, count)
		}
	}

	// Channel 21 is referenced by two items (11, 13) but, sharing the same
	// model, must be looked up only once. Channel 22 backs two distinct models
	// so it is looked up twice.
	if calls[21] != 1 {
		t.Fatalf("channel 21 looked up %d times, want 1", calls[21])
	}
	if calls[22] != 2 {
		t.Fatalf("channel 22 looked up %d times, want 2", calls[22])
	}
}

func TestCollectCompactProbeJobsSkipsInvalidItems(t *testing.T) {
	groups := []compactProbeGroup{
		{
			ID:   1,
			Name: "mixed",
			Items: []model.GroupItem{
				{ID: 0, GroupID: 1, ChannelID: 21, ModelName: "gpt-5.5"},  // zero ID
				{ID: 12, GroupID: 1, ChannelID: 0, ModelName: "gpt-5.5"},  // zero channel
				{ID: 13, GroupID: 1, ChannelID: 22, ModelName: "   "},     // blank model
				{ID: 14, GroupID: 1, ChannelID: 23, ModelName: "gpt-5.5"}, // valid
			},
		},
	}

	calls := map[int]int{}
	jobs, _, canceled := collectCompactProbeJobs(context.Background(), groups, fakeChannelGetter(calls, nil))
	if canceled {
		t.Fatal("unexpected cancellation")
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 valid job, got %d: %+v", len(jobs), jobs)
	}
	if jobs[0].key.ChannelID != 23 {
		t.Fatalf("expected the only job to be channel 23, got %+v", jobs[0].key)
	}
	if _, looked := calls[21]; looked {
		t.Fatal("channel 21 should never be looked up (item has zero ID)")
	}
}

func TestCollectCompactProbeJobsRecordsChannelLookupErrors(t *testing.T) {
	groups := []compactProbeGroup{
		{
			ID:   1,
			Name: "g",
			Items: []model.GroupItem{
				{ID: 11, GroupID: 1, ChannelID: 21, ModelName: "gpt-5.5"},
				{ID: 12, GroupID: 1, ChannelID: 99, ModelName: "gpt-5.5"},
			},
		},
	}

	wantErr := errors.New("channel gone")
	jobs, setupErrors, _ := collectCompactProbeJobs(
		context.Background(),
		groups,
		fakeChannelGetter(map[int]int{}, map[int]error{99: wantErr}),
	)

	if len(jobs) != 1 || jobs[0].key.ChannelID != 21 {
		t.Fatalf("expected only channel 21 to become a job, got %+v", jobs)
	}
	key := compactProbeKey{ChannelID: 99, ModelName: "gpt-5.5"}
	if got := setupErrors[key]; !errors.Is(got, wantErr) {
		t.Fatalf("expected setup error for %+v, got %v", key, got)
	}
}

func TestCollectCompactProbeJobsStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	groups := []compactProbeGroup{
		{ID: 1, Name: "g", Items: []model.GroupItem{{ID: 11, GroupID: 1, ChannelID: 21, ModelName: "gpt-5.5"}}},
	}

	calls := map[int]int{}
	jobs, _, canceled := collectCompactProbeJobs(ctx, groups, fakeChannelGetter(calls, nil))
	if !canceled {
		t.Fatal("expected canceled=true for a canceled context")
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs from a canceled context, got %d", len(jobs))
	}
	if len(calls) != 0 {
		t.Fatal("expected no channel lookups for a canceled context")
	}
}
