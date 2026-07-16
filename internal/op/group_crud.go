package op

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func GroupCreate(group *model.Group, ctx context.Context) error {
	if err := model.ValidateGroup(group); err != nil {
		return fmt.Errorf("%w: invalid group: %v", ErrInvalidInput, err)
	}
	if _, exists := groupMap.Get(group.Name); exists {
		return fmt.Errorf("%w: group name already exists", ErrConflict)
	}
	// 新建分组默认启用：bool 零值为 false，create payload 未带 enabled 时强制为 true，避免误建成禁用。
	group.Enabled = true

	items := group.Items
	group.Items = nil

	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		group.Items = items
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	if err := tx.Omit("Items").Create(group).Error; err != nil {
		tx.Rollback()
		group.Items = items
		return err
	}

	for i := range items {
		items[i].GroupID = group.ID
		items[i].Type = normalizeGroupItemType(items[i].Type)
		items[i].ModelName = strings.TrimSpace(items[i].ModelName)
		if err := validateGroupItemFields(tx, group.ID, items[i].Type, items[i].ChannelID, items[i].TargetGroupID, items[i].ModelName); err != nil {
			tx.Rollback()
			group.Items = items
			return fmt.Errorf("%w: invalid group item: %v", ErrInvalidInput, err)
		}
	}

	if len(items) > 0 {
		if err := tx.Create(&items).Error; err != nil {
			tx.Rollback()
			group.Items = items
			return err
		}
	}

	if err := tx.Commit().Error; err != nil {
		group.Items = items
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	group.Items = items
	groupCache.Set(group.ID, *group)
	groupMap.Set(group.Name, *group)
	return nil
}

func GroupUpdate(req *model.GroupUpdateRequest, ctx context.Context) (*model.Group, error) {
	if err := model.ValidateGroupUpdate(req); err != nil {
		return nil, fmt.Errorf("%w: invalid group update: %v", ErrInvalidInput, err)
	}
	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("%w: group not found", ErrNotFound)
	}
	if req.Name != nil {
		if existing, exists := groupMap.Get(*req.Name); exists && existing.ID != req.ID {
			return nil, fmt.Errorf("%w: group name already exists", ErrConflict)
		}
	}
	oldName := oldGroup.Name

	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	if containsNestedGroupItemAdd(req.ItemsToAdd) {
		if err := lockGroupRowsForNestingTx(tx); err != nil {
			tx.Rollback()
			return nil, err
		}
	}

	if err := applyGroupPatchTx(tx, req); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := deleteGroupItemsTx(tx, req.ID, req.ItemsToDelete); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := updateGroupItemsTx(tx, req.ID, req.ItemsToUpdate); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := addGroupItemsTx(tx, req.ID, req.ItemsToAdd); err != nil {
		tx.Rollback()
		return nil, err
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 刷新缓存并返回最新数据
	if err := groupRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, err
	}

	group, _ := groupCache.Get(req.ID)
	if oldName != "" && oldName != group.Name {
		groupMap.Del(oldName)
	}
	return &group, nil
}

func applyGroupPatchTx(tx *gorm.DB, req *model.GroupUpdateRequest) error {
	helper := NewPatchHelper()
	helper.ApplyField("name", req.Name)
	helper.ApplyField("enabled", req.Enabled)
	helper.ApplyField("mode", req.Mode)
	helper.ApplyField("match_regex", req.MatchRegex)
	helper.ApplyField("first_token_time_out", req.FirstTokenTimeOut)
	helper.ApplyField("session_keep_time", req.SessionKeepTime)

	if !helper.HasUpdates() {
		return nil
	}
	result := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(helper.SelectFields()).Updates(helper.Updates())
	if result.Error != nil {
		return fmt.Errorf("failed to update group: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify group update: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("%w: group not found", ErrNotFound)
		}
	}
	return nil
}

func deleteGroupItemsTx(tx *gorm.DB, groupID int, itemIDs []int) error {
	if len(itemIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&model.GroupItem{}).Where("id IN ? AND group_id = ?", itemIDs, groupID).Count(&count).Error; err != nil {
		return fmt.Errorf("failed to verify items to delete: %w", err)
	}
	if count != int64(len(itemIDs)) {
		return fmt.Errorf("%w: one or more group items to delete were not found", ErrNotFound)
	}
	if err := tx.Where("id IN ? AND group_id = ?", itemIDs, groupID).Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("failed to delete items: %w", err)
	}
	return nil
}

func updateGroupItemsTx(tx *gorm.DB, groupID int, items []model.GroupItemUpdateRequest) error {
	if len(items) == 0 {
		return nil
	}
	ids, updateColumns := buildGroupItemUpdates(items)
	var count int64
	if err := tx.Model(&model.GroupItem{}).Where("id IN ? AND group_id = ?", ids, groupID).Count(&count).Error; err != nil {
		return fmt.Errorf("failed to verify items to update: %w", err)
	}
	if count != int64(len(ids)) {
		return fmt.Errorf("%w: one or more group items to update were not found", ErrNotFound)
	}
	if err := tx.Model(&model.GroupItem{}).
		Where("id IN ? AND group_id = ?", ids, groupID).
		Updates(updateColumns).Error; err != nil {
		return fmt.Errorf("failed to update items: %w", err)
	}
	return nil
}

func buildGroupItemUpdates(items []model.GroupItemUpdateRequest) ([]int, map[string]interface{}) {
	ids := make([]int, len(items))
	priorityCase := "CASE id"
	weightCase := "CASE id"
	disabledCase := "CASE id"
	hasPriorityChange := false
	hasWeightChange := false
	hasDisabledChange := false
	for i, item := range items {
		ids[i] = item.ID
		if item.Priority != nil {
			hasPriorityChange = true
			priorityCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, *item.Priority)
		}
		if item.Weight != nil {
			hasWeightChange = true
			weightCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, *item.Weight)
		}
		if item.Disabled != nil {
			hasDisabledChange = true
			disabledCase += fmt.Sprintf(" WHEN %d THEN %t", item.ID, *item.Disabled)
		}
	}
	updateColumns := make(map[string]interface{}, 3)
	if hasPriorityChange {
		updateColumns["priority"] = gorm.Expr(priorityCase + " ELSE priority END")
	}
	if hasWeightChange {
		updateColumns["weight"] = gorm.Expr(weightCase + " ELSE weight END")
	}
	if hasDisabledChange {
		updateColumns["disabled"] = gorm.Expr(disabledCase + " ELSE disabled END")
	}
	return ids, updateColumns
}

func addGroupItemsTx(tx *gorm.DB, groupID int, items []model.GroupItemAddRequest) error {
	if len(items) == 0 {
		return nil
	}
	newItems := make([]model.GroupItem, len(items))
	for i, item := range items {
		itemType := normalizeGroupItemType(item.Type)
		modelName := strings.TrimSpace(item.ModelName)
		if err := validateGroupItemFields(tx, groupID, itemType, item.ChannelID, item.TargetGroupID, modelName); err != nil {
			return fmt.Errorf("%w: invalid group item: %v", ErrInvalidInput, err)
		}
		newItems[i] = model.GroupItem{
			GroupID:       groupID,
			Type:          itemType,
			ChannelID:     item.ChannelID,
			TargetGroupID: item.TargetGroupID,
			ModelName:     modelName,
			Priority:      item.Priority,
			Weight:        item.Weight,
		}
	}
	if err := tx.Create(&newItems).Error; err != nil {
		return fmt.Errorf("failed to create items: %w", err)
	}
	return nil
}

func GroupDel(id int, ctx context.Context) error {
	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	if err := lockGroupRowsForNestingTx(tx); err != nil {
		tx.Rollback()
		return err
	}

	var group model.Group
	if err := tx.First(&group, id).Error; err != nil {
		tx.Rollback()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: group not found", ErrNotFound)
		}
		return fmt.Errorf("failed to load group: %w", err)
	}

	refs, err := findReferencingGroupItemsTx(tx, id)
	if err != nil {
		tx.Rollback()
		return err
	}
	if len(refs) > 0 {
		tx.Rollback()
		groupNames := make([]string, 0, len(refs))
		for _, ref := range refs {
			// ref format: "GroupName(item:123)"
			// Extract just the group name
			if idx := strings.Index(ref, "("); idx > 0 {
				groupNames = append(groupNames, ref[:idx])
			} else {
				groupNames = append(groupNames, ref)
			}
		}
		if len(groupNames) == 1 {
			return fmt.Errorf("%w: 无法删除：该分组正被「%s」引用。请先从该分组中移除此引用，或删除「%s」分组", ErrConflict, groupNames[0], groupNames[0])
		}
		return fmt.Errorf("%w: 无法删除：该分组正被 %d 个分组引用（%s）。请先移除所有引用关系", ErrConflict, len(groupNames), strings.Join(groupNames, "、"))
	}

	if err := tx.Where("group_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	result := tx.Delete(&model.Group{}, id)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		tx.Rollback()
		return fmt.Errorf("%w: group not found", ErrNotFound)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	groupCache.Del(id)
	groupMap.Del(group.Name)
	return nil
}
