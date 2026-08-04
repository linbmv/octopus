package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/channelstate"
	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/routingstate"
	"github.com/bestruirui/octopus/internal/utils/diff"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

var (
	ErrSyncModelsInProgress = errors.New("model synchronization is already running")
	syncModelsMu            sync.Mutex
	syncModelsStateMu       sync.RWMutex
	lastSyncModelsTime      time.Time
)

func SyncModelsTaskContext(ctx context.Context) error {
	return SyncModelsNow(ctx)
}

// SyncModelsNow runs one synchronization pass. Manual and scheduled calls share
// the same non-blocking lock, so a slow upstream cannot start overlapping runs.
func SyncModelsNow(parent context.Context) error {
	if !syncModelsMu.TryLock() {
		return ErrSyncModelsInProgress
	}
	defer syncModelsMu.Unlock()

	log.Debugf("sync models task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("sync models task finished, sync time: %s", time.Since(startTime))
	}()
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()

	channels, err := op.ChannelList(ctx)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}

	totalNewModels, channelErr := syncAllChannels(channels, ctx)
	if channelErr != nil {
		return channelErr
	}
	if err := syncPriceDatabase(totalNewModels, ctx); err != nil {
		return err
	}
	syncModelsStateMu.Lock()
	lastSyncModelsTime = time.Now()
	syncModelsStateMu.Unlock()
	return nil
}

func syncAllChannels(channels []model.Channel, ctx context.Context) ([]string, error) {
	totalNewModels := make([]string, 0, 128)
	seenTotalNewModels := make(map[string]struct{}, 128)
	errs := make([]error, 0)

	for _, channel := range channels {
		if !channel.AutoSync {
			continue
		}
		newModels, err := syncSingleChannel(&channel, ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("channel %s (%d): %w", channel.Name, channel.ID, err))
			continue
		}
		collectUniqueModels(newModels, &totalNewModels, seenTotalNewModels)
	}
	return totalNewModels, errors.Join(errs...)
}

func syncSingleChannel(channel *model.Channel, ctx context.Context) ([]string, error) {
	fetchModels, err := helper.FetchModels(ctx, *channel)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	newModels := xstrings.TrimCompact(fetchModels)

	// 如果获取到空列表且渠道原本有模型，跳过更新以防误删（可能是 API key 权限不足或临时故障）
	if len(fetchModels) == 0 && channel.Model != "" {
		return nil, errors.New("fetched an empty model list; existing models were preserved")
	}

	oldModels := xstrings.SplitTrimCompact(",", channel.Model)
	deletedModels, addedModels := diff.Diff(oldModels, newModels)

	if len(deletedModels) > 0 || len(addedModels) > 0 {
		if err := updateChannelModels(channel, newModels, ctx); err != nil {
			return nil, err
		}
	}
	if len(deletedModels) > 0 {
		if err := deleteRemovedModelsFromGroups(channel.ID, channel.Name, deletedModels, ctx); err != nil {
			return nil, err
		}
	}
	if len(newModels) > 0 {
		helper.ChannelAutoGroup(channel, ctx)
	}
	return newModels, nil
}

func updateChannelModels(channel *model.Channel, newModels []string, ctx context.Context) error {
	fetchModelStr := strings.Join(newModels, ",")
	updated, err := op.ChannelUpdate(&model.ChannelUpdateRequest{
		ID:    channel.ID,
		Model: &fetchModelStr,
	}, ctx)
	if err != nil {
		return fmt.Errorf("update channel models: %w", err)
	}
	channelstate.Invalidate(channel.ID, channel, updated)
	channel.Model = fetchModelStr
	return nil
}

func deleteRemovedModelsFromGroups(channelID int, channelName string, deletedModels []string, ctx context.Context) error {
	log.Infof("deleted channel %s models: %v", channelName, deletedModels)
	keys := make([]model.GroupIDAndLLMName, len(deletedModels))
	for i, m := range deletedModels {
		keys[i] = model.GroupIDAndLLMName{ChannelID: channelID, ModelName: m}
	}
	changed, err := op.GroupItemBatchDelByChannelAndModels(keys, ctx)
	if err != nil {
		return fmt.Errorf("delete removed group items: %w", err)
	}
	if changed {
		balancer.InvalidateGroups()
		routingstate.Notify()
	}
	return nil
}

func collectUniqueModels(newModels []string, totalNewModels *[]string, seenTotalNewModels map[string]struct{}) {
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
		*totalNewModels = append(*totalNewModels, m)
	}
}

func syncPriceDatabase(totalNewModels []string, ctx context.Context) error {
	llmPrice, err := op.LLMList(ctx)
	if err != nil {
		return fmt.Errorf("list model prices: %w", err)
	}
	llmPriceNames := make([]string, 0, len(llmPrice))
	for _, price := range llmPrice {
		llmPriceNames = append(llmPriceNames, price.Name)
	}

	deletedNorm, addedNorm := diff.Diff(llmPriceNames, totalNewModels)
	if len(deletedNorm) > 0 {
		if err := helper.LLMPriceDeleteFromDBWithNoPrice(deletedNorm, ctx); err != nil {
			return fmt.Errorf("delete stale model prices: %w", err)
		}
	}
	if len(addedNorm) > 0 {
		if err := helper.LLMPriceAddToDB(addedNorm, ctx); err != nil {
			return fmt.Errorf("add model prices: %w", err)
		}
	}
	return nil
}

func GetLastSyncModelsTime() time.Time {
	syncModelsStateMu.RLock()
	defer syncModelsStateMu.RUnlock()
	return lastSyncModelsTime
}
