package task

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/diff"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

var lastSyncModelsTime = time.Now()

const compactProbeMaxConcurrency = 5

type compactProbeGroup struct {
	ID    int
	Name  string
	Items []model.GroupItem
}

type compactProbeKey struct {
	ChannelID int
	ModelName string
}

type compactProbeJob struct {
	key       compactProbeKey
	channel   model.Channel
	modelName string
}

type compactProbeSummary struct {
	considered int
	probed     int
	succeeded  int
	skipped    int
	failed     int
}

type compactProbeItemOutcome int

const (
	compactProbeItemOutcomeSucceeded compactProbeItemOutcome = iota
	compactProbeItemOutcomeSkipped
	compactProbeItemOutcomeFailed
)

// SyncModelsTask 同步模型任务
func SyncModelsTask() {
	log.Debugf("sync models task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("sync models task finished, sync time: %s", time.Since(startTime))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	channels, err := op.ChannelList(ctx)
	if err != nil {
		log.Errorf("failed to list channels: %v", err)
		return
	}
	totalNewModels := make([]string, 0, 128)
	seenTotalNewModels := make(map[string]struct{}, 128)
	for _, channel := range channels {
		if !channel.AutoSync {
			continue
		}
		fetchModels, err := helper.FetchModels(ctx, channel)
		if err != nil {
			log.Warnf("failed to fetch models for channel %s: %v", channel.Name, err)
			continue
		}
		newModels := xstrings.TrimCompact(fetchModels)

		// 如果获取到空列表且渠道原本有模型，跳过更新以防误删（可能是 API key 权限不足或临时故障）
		if len(fetchModels) == 0 && channel.Model != "" {
			log.Warnf("channel %s fetched empty model list but has existing models, skipping sync to prevent data loss", channel.Name)
			continue
		}
		oldModels := xstrings.SplitTrimCompact(",", channel.Model)
		for _, m := range newModels {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			m = strings.ToLower(m)
			if _, ok := seenTotalNewModels[m]; ok {
				continue
			}
			seenTotalNewModels[m] = struct{}{}
			totalNewModels = append(totalNewModels, m)
		}
		deletedModels, addedModels := diff.Diff(oldModels, newModels)
		if len(deletedModels) > 0 || len(addedModels) > 0 {
			fetchModelStr := strings.Join(newModels, ",")
			if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{
				ID:    channel.ID,
				Model: &fetchModelStr,
			}, ctx); err != nil {
				log.Errorf("failed to update channel %s: %v", channel.Name, err)
				continue
			}
			channel.Model = fetchModelStr
		}
		// 批量删除消失的模型对应的 GroupItem
		if len(deletedModels) > 0 {
			log.Infof("deleted channel %s models: %v", channel.Name, deletedModels)
			keys := make([]model.GroupIDAndLLMName, len(deletedModels))
			for i, m := range deletedModels {
				keys[i] = model.GroupIDAndLLMName{ChannelID: channel.ID, ModelName: m}
			}
			if err := op.GroupItemBatchDelByChannelAndModels(keys, ctx); err != nil {
				log.Errorf("failed to batch delete group items for channel %s: %v", channel.Name, err)
			}
		}

		// 自动分组
		if len(newModels) > 0 {
			helper.ChannelAutoGroup(&channel, ctx)
		}
	}
	syncCompactGroupStrategies(ctx)

	llmPrice, err := op.LLMList(ctx)
	if err != nil {
		log.Errorf("failed to list models price: %v", err)
		return
	}
	llmPriceNames := make([]string, 0, len(llmPrice))
	for _, price := range llmPrice {
		llmPriceNames = append(llmPriceNames, price.Name)
	}

	deletedNorm, addedNorm := diff.Diff(llmPriceNames, totalNewModels)
	if len(deletedNorm) > 0 {
		if err := helper.LLMPriceDeleteFromDBWithNoPrice(deletedNorm, ctx); err != nil {
			log.Errorf("failed to batch delete models price: %v", err)
		}
	}
	if len(addedNorm) > 0 {
		if err := helper.LLMPriceAddToDB(addedNorm, ctx); err != nil {
			log.Errorf("failed to add models price: %v", err)
		}
	}
	lastSyncModelsTime = time.Now()
}

func GetLastSyncModelsTime() time.Time {
	return lastSyncModelsTime
}

func syncCompactGroupStrategies(ctx context.Context) {
	startTime := time.Now()
	summary := compactProbeSummary{}
	defer func() {
		log.Infof(
			"compact strategy probe summary: considered=%d, probed=%d, succeeded=%d, skipped=%d, failed=%d, elapsed=%s",
			summary.considered,
			summary.probed,
			summary.succeeded,
			summary.skipped,
			summary.failed,
			time.Since(startTime),
		)
	}()

	groups, err := op.GroupList(ctx)
	if err != nil {
		log.Warnf("failed to list groups for compact strategy probe: %v", err)
		return
	}

	probeGroups := make([]compactProbeGroup, 0, len(groups))

	for _, group := range groups {
		if ctx.Err() != nil {
			return
		}
		if !group.Enabled {
			continue
		}
		expanded, err := op.GroupGetEnabledByID(group.ID, ctx)
		if err != nil {
			log.Debugf("failed to get compact probe group %d: %v", group.ID, err)
			continue
		}
		probeGroups = append(probeGroups, compactProbeGroup{
			ID:    expanded.ID,
			Name:  expanded.Name,
			Items: expanded.Items,
		})
	}

	probeResults, setupErrors, probed := runCompactStrategyProbes(ctx, probeGroups)
	summary.probed = probed

	for _, group := range probeGroups {
		probeCompactGroupStrategies(ctx, group, probeResults, setupErrors, &summary)
	}
}

// collectCompactProbeJobs flattens all group items into a deduplicated job list:
// each distinct (channelID, modelName) is probed at most once even when it is
// referenced by many groups/items, while the per-item DB write still fans the
// result out to every referencing item. Items that fail channel lookup are
// recorded in setupErrors so the caller can mark them skipped without probing.
// channelGetter is injected so the dedup logic can be unit-tested without the
// op/DB layer. Returns early if ctx is canceled mid-collection.
func collectCompactProbeJobs(
	ctx context.Context,
	groups []compactProbeGroup,
	channelGetter func(int) (*model.Channel, error),
) ([]compactProbeJob, map[compactProbeKey]error, bool) {
	seen := make(map[compactProbeKey]struct{})
	setupErrors := make(map[compactProbeKey]error)
	jobs := make([]compactProbeJob, 0)
	for _, group := range groups {
		for _, item := range group.Items {
			if ctx.Err() != nil {
				return jobs, setupErrors, true
			}
			if item.ID == 0 || item.ChannelID == 0 || strings.TrimSpace(item.ModelName) == "" {
				continue
			}
			key := compactProbeKey{ChannelID: item.ChannelID, ModelName: item.ModelName}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			channel, err := channelGetter(item.ChannelID)
			if err != nil {
				setupErrors[key] = err
				continue
			}
			jobs = append(jobs, compactProbeJob{
				key:       key,
				channel:   *channel,
				modelName: item.ModelName,
			})
		}
	}
	return jobs, setupErrors, false
}

func runCompactStrategyProbes(ctx context.Context, groups []compactProbeGroup) (map[compactProbeKey]helper.CompactProbeResult, map[compactProbeKey]error, int) {
	jobs, setupErrors, canceled := collectCompactProbeJobs(ctx, groups, func(channelID int) (*model.Channel, error) {
		return op.ChannelGet(channelID, ctx)
	})
	if canceled {
		return map[compactProbeKey]helper.CompactProbeResult{}, setupErrors, 0
	}

	results := make(map[compactProbeKey]helper.CompactProbeResult, len(jobs))
	if len(jobs) == 0 {
		return results, setupErrors, 0
	}

	jobsCh := make(chan compactProbeJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	probed := 0
	workerCount := compactProbeMaxConcurrency
	if len(jobs) < workerCount {
		workerCount = len(jobs)
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobsCh {
				if ctx.Err() != nil {
					continue
				}
				result := helper.ProbeCompactStrategy(ctx, job.channel, job.modelName)
				mu.Lock()
				results[job.key] = result
				probed++
				mu.Unlock()
			}
		}()
	}

	sendCanceled := false
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			sendCanceled = true
		case jobsCh <- job:
		}
		if sendCanceled {
			break
		}
	}
	close(jobsCh)
	wg.Wait()

	return results, setupErrors, probed
}

func probeCompactGroupStrategies(
	ctx context.Context,
	group compactProbeGroup,
	probeResults map[compactProbeKey]helper.CompactProbeResult,
	setupErrors map[compactProbeKey]error,
	summary *compactProbeSummary,
) {
	updatedGroupIDs := make(map[int]struct{})
	for _, item := range group.Items {
		if summary != nil {
			summary.considered++
		}
		updated, outcome := syncGroupItemCompactStrategy(ctx, group.Name, item, probeResults, setupErrors)
		if updated {
			updatedGroupIDs[item.GroupID] = struct{}{}
		}
		if summary == nil {
			continue
		}
		switch outcome {
		case compactProbeItemOutcomeSucceeded:
			summary.succeeded++
		case compactProbeItemOutcomeFailed:
			summary.failed++
		default:
			summary.skipped++
		}
	}
	for updatedGroupID := range updatedGroupIDs {
		if err := op.GroupRefreshCacheByID(updatedGroupID, ctx); err != nil {
			log.Warnf("failed to refresh compact probe group cache %d: %v", updatedGroupID, err)
		}
	}
}

func syncGroupItemCompactStrategy(
	ctx context.Context,
	groupName string,
	item model.GroupItem,
	probeResults map[compactProbeKey]helper.CompactProbeResult,
	setupErrors map[compactProbeKey]error,
) (bool, compactProbeItemOutcome) {
	if item.ID == 0 || item.ChannelID == 0 || strings.TrimSpace(item.ModelName) == "" {
		return false, compactProbeItemOutcomeSkipped
	}
	key := compactProbeKey{ChannelID: item.ChannelID, ModelName: item.ModelName}
	if err, ok := setupErrors[key]; ok {
		log.Debugf("failed to get compact probe channel %d for group %s: %v", item.ChannelID, groupName, err)
		return false, compactProbeItemOutcomeSkipped
	}
	result, ok := probeResults[key]
	if !ok {
		if ctx.Err() != nil {
			log.Debugf("group %s item %d compact strategy probe skipped: %v", groupName, item.ID, ctx.Err())
		}
		return false, compactProbeItemOutcomeSkipped
	}
	if err := op.GroupItemCompactStrategyUpdateNoCacheRefresh(item.ID, item.GroupID, result.Strategy, result.Error, time.Now(), ctx); err != nil {
		log.Warnf("failed to update compact strategy for group %s item %d: %v", groupName, item.ID, err)
		return false, compactProbeItemOutcomeFailed
	}
	if result.Strategy != "" {
		log.Infof("group %s item %d compact strategy detected: %s", groupName, item.ID, result.Strategy)
		return true, compactProbeItemOutcomeSucceeded
	}
	if result.Error == "" {
		return true, compactProbeItemOutcomeSkipped
	}
	if strings.HasPrefix(result.Error, "compact probe skipped:") {
		log.Debugf("group %s item %d compact strategy probe skipped: %s", groupName, item.ID, result.Error)
		return true, compactProbeItemOutcomeSkipped
	}
	log.Debugf("group %s item %d compact strategy probe failed: %s", groupName, item.ID, result.Error)
	return true, compactProbeItemOutcomeFailed
}
