package op

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm/clause"
)

func GroupItemBatchAdd(groupID int, items []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(items) == 0 {
		return nil
	}

	group, ok := groupCache.Get(groupID)
	if !ok {
		return fmt.Errorf("group not found")
	}

	seen := make(map[string]struct{}, len(items))
	uniq := make([]model.GroupIDAndLLMName, 0, len(items))
	for _, it := range items {
		if it.ChannelID == 0 || it.ModelName == "" {
			continue
		}
		k := fmt.Sprintf("%d|%s", it.ChannelID, it.ModelName)
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, it)
	}
	if len(uniq) == 0 {
		return nil
	}

	nextPriority := 1
	for _, gi := range group.Items {
		if gi.Priority >= nextPriority {
			nextPriority = gi.Priority + 1
		}
	}

	newItems := make([]model.GroupItem, 0, len(uniq))
	for _, it := range uniq {
		newItems = append(newItems, model.GroupItem{
			GroupID:   groupID,
			Type:      model.GroupItemTypeChannel,
			ChannelID: it.ChannelID,
			ModelName: it.ModelName,
			Priority:  nextPriority,
			Weight:    1,
		})
		nextPriority++
	}

	if err := db.GetDB().WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "group_id"},
				{Name: "type"},
				{Name: "channel_id"},
				{Name: "target_group_id"},
				{Name: "model_name"},
			},
			DoNothing: true,
		}).
		Create(&newItems).Error; err != nil {
		return fmt.Errorf("failed to create group items: %w", err)
	}

	return groupRefreshCacheByID(groupID, ctx)
}

func GroupItemCompactStrategyUpdate(itemID, groupID int, strategy model.CompactStrategy, updatedAt time.Time, ctx context.Context) error {
	if err := GroupItemCompactStrategyUpdateNoCacheRefresh(itemID, groupID, strategy, updatedAt, ctx); err != nil {
		return err
	}
	return groupRefreshCacheByID(groupID, ctx)
}

func GroupItemCompactStrategyUpdateNoCacheRefresh(itemID, groupID int, strategy model.CompactStrategy, updatedAt time.Time, ctx context.Context) error {
	if itemID == 0 || groupID == 0 {
		return fmt.Errorf("invalid group item")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	updates := map[string]any{
		"compact_strategy":            strategy,
		"compact_strategy_updated_at": updatedAt,
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.GroupItem{}).Where("id = ?", itemID).Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update group item compact strategy: %w", err)
	}
	return nil
}

// GroupItemBatchDelByChannelAndModels 根据渠道ID和模型名称批量删除分组项
func GroupItemBatchDelByChannelAndModels(keys []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(keys) == 0 {
		return nil
	}

	conditions := make([][]interface{}, len(keys))
	for i, key := range keys {
		conditions[i] = []interface{}{key.ChannelID, key.ModelName}
	}

	var groupIDs []int
	if err := db.GetDB().WithContext(ctx).
		Model(&model.GroupItem{}).
		Distinct("group_id").
		Where("(channel_id, model_name) IN ?", conditions).
		Pluck("group_id", &groupIDs).Error; err != nil {
		return fmt.Errorf("failed to find group ids: %w", err)
	}

	if len(groupIDs) == 0 {
		return nil
	}

	if err := db.GetDB().WithContext(ctx).
		Where("(channel_id, model_name) IN ?", conditions).
		Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := groupRefreshCacheByIDs(groupIDs, ctx); err != nil {
		return fmt.Errorf("failed to refresh group cache: %w", err)
	}

	return nil
}

func GroupItemPruneByChannelModels(channelID int, modelNames []string, ctx context.Context) error {
	if channelID == 0 {
		return nil
	}

	allowed := make([]string, 0, len(modelNames))
	seen := make(map[string]struct{}, len(modelNames))
	for _, modelName := range modelNames {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		key := strings.ToLower(modelName)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		allowed = append(allowed, key)
	}

	query := db.GetDB().WithContext(ctx).
		Model(&model.GroupItem{}).
		Where("type = ? AND channel_id = ?", model.GroupItemTypeChannel, channelID)
	if len(allowed) > 0 {
		query = query.Where("LOWER(model_name) NOT IN ?", allowed)
	}

	var groupIDs []int
	if err := query.Distinct("group_id").Pluck("group_id", &groupIDs).Error; err != nil {
		return fmt.Errorf("failed to find stale group ids: %w", err)
	}
	if len(groupIDs) == 0 {
		return nil
	}

	deleteQuery := db.GetDB().WithContext(ctx).
		Where("type = ? AND channel_id = ?", model.GroupItemTypeChannel, channelID)
	if len(allowed) > 0 {
		deleteQuery = deleteQuery.Where("LOWER(model_name) NOT IN ?", allowed)
	}
	if err := deleteQuery.Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("failed to delete stale group items: %w", err)
	}

	if err := groupRefreshCacheByIDs(groupIDs, ctx); err != nil {
		return fmt.Errorf("failed to refresh group cache: %w", err)
	}

	return nil
}

func GroupItemList(groupID int, ctx context.Context) ([]model.GroupItem, error) {
	var items []model.GroupItem
	if err := db.GetDB().WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("priority ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func groupRefreshCache(ctx context.Context) error {
	groups := []model.Group{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Find(&groups).Error; err != nil {
		return err
	}
	groupCache.Clear()
	groupMap.Clear()
	for _, group := range groups {
		groupCache.Set(group.ID, group)
		groupMap.Set(group.Name, group)
	}
	return nil
}

func groupRefreshCacheByID(id int, ctx context.Context) error {
	var group model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		First(&group, id).Error; err != nil {
		return err
	}
	groupCache.Set(group.ID, group)
	groupMap.Set(group.Name, group)
	return nil
}

func groupRefreshCacheByIDs(ids []int, ctx context.Context) error {
	if len(ids) == 0 {
		return nil
	}
	var groups []model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Where("id IN ?", ids).
		Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		groupCache.Set(group.ID, group)
		groupMap.Set(group.Name, group)
	}
	return nil
}
