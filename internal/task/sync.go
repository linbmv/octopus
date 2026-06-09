package task

import (
	"context"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/diff"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

var lastSyncModelsTime = time.Now()

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
	groups, err := op.GroupList(ctx)
	if err != nil {
		log.Warnf("failed to list groups for compact strategy probe: %v", err)
		return
	}

	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		probeCompactGroupStrategies(ctx, group.ID)
	}
}

func probeCompactGroupStrategies(ctx context.Context, groupID int) {
	expanded, err := op.GroupGetEnabledByID(groupID, ctx)
	if err != nil {
		log.Debugf("failed to get compact probe group %d: %v", groupID, err)
		return
	}
	for _, item := range expanded.Items {
		syncGroupItemCompactStrategy(ctx, expanded.Name, item)
	}
}

func syncGroupItemCompactStrategy(ctx context.Context, groupName string, item model.GroupItem) {
	if item.ID == 0 || item.ChannelID == 0 || strings.TrimSpace(item.ModelName) == "" {
		return
	}
	channel, err := op.ChannelGet(item.ChannelID, ctx)
	if err != nil {
		log.Debugf("failed to get compact probe channel %d for group %s: %v", item.ChannelID, groupName, err)
		return
	}
	result := helper.ProbeCompactStrategy(ctx, *channel, item.ModelName)
	if err := op.GroupItemCompactStrategyUpdate(item.ID, item.GroupID, result.Strategy, result.Error, time.Now(), ctx); err != nil {
		log.Warnf("failed to update compact strategy for group %s item %d: %v", groupName, item.ID, err)
		return
	}
	if result.Strategy != "" {
		log.Infof("group %s item %d compact strategy detected: %s", groupName, item.ID, result.Strategy)
		return
	}
	if result.Error == "" {
		return
	}
	if strings.HasPrefix(result.Error, "compact probe skipped:") {
		log.Debugf("group %s item %d compact strategy probe skipped: %s", groupName, item.ID, result.Error)
		return
	}
	log.Debugf("group %s item %d compact strategy probe failed: %s", groupName, item.ID, result.Error)
}
