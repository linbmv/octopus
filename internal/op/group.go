package op

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm"
)

const MaxGroupNestDepth = 3

var (
	groupCache      = cache.New[int, model.Group](16) // 按主键保存完整分组配置。
	groupNameIndex  = cache.New[string, int](16)      // 客户端模型名对应的分组主键。
	groupMutationMu sync.Mutex                        // 串行化图结构变更，避免并发写入分别通过环检测。
)

// GroupList 返回缓存中的全部分组。
func GroupList() []model.Group {
	groups := make([]model.Group, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		groups = append(groups, groupSnapshot(group))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups
}

// GroupListModel 返回全部已启用的分组模型名。
func GroupListModel() []string {
	models := make([]string, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		if group.Enabled {
			models = append(models, group.Name)
		}
	}
	sort.Strings(models)
	return models
}

// GroupGetByName 返回客户端模型名称对应的完整分组配置，包括临时禁用的分组和成员。
func GroupGetByName(name string) (model.Group, error) {
	groupID, ok := groupNameIndex.Get(name)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return GroupGetByID(groupID)
}

// GroupGetByID 返回主键对应的完整分组配置。
func GroupGetByID(id int) (model.Group, error) {
	group, ok := groupCache.Get(id)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return groupSnapshot(group), nil
}

// GroupGetEnabledByName 返回可参与 Relay 的根分组。
func GroupGetEnabledByName(name string) (model.Group, error) {
	group, err := GroupGetByName(name)
	if err != nil || !group.Enabled {
		return model.Group{}, fmt.Errorf("group not found")
	}
	return group, nil
}

// GroupCreate 创建分组及其成员并刷新缓存。
func GroupCreate(group *model.Group, ctx context.Context) error {
	if group == nil {
		return fmt.Errorf("group is required")
	}
	groupMutationMu.Lock()
	defer groupMutationMu.Unlock()

	group.ID = 0
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		return fmt.Errorf("group name is required")
	}
	group.Enabled = true
	group.ActiveItemID = 0
	if group.Mode == "" {
		group.Mode = model.GroupModeManual
	}
	if err := validateGroupMode(group.Mode); err != nil {
		return err
	}
	model.NormalizeGroupRelayConfig(&group.RelayConfig)

	items := slices.Clone(group.Items)
	for i := range items {
		items[i].ID = 0
		items[i].GroupID = 0
		if items[i].Weight <= 0 {
			items[i].Weight = 1
		}
		if err := normalizeAndValidateGroupItem(&items[i]); err != nil {
			return fmt.Errorf("item %d: %w", i+1, err)
		}
	}

	var saved model.Group
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := *group
		base.Items = nil
		if err := tx.Create(&base).Error; err != nil {
			return fmt.Errorf("failed to create group: %w", err)
		}
		for i := range items {
			items[i].GroupID = base.ID
		}
		if len(items) > 0 {
			if err := tx.Create(&items).Error; err != nil {
				return fmt.Errorf("failed to create group items: %w", err)
			}
		}
		if err := validateGroupGraph(tx); err != nil {
			return err
		}
		if err := tx.Preload("Items").First(&saved, base.ID).Error; err != nil {
			return fmt.Errorf("failed to load created group: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	sortGroupItems(saved.Items)
	snapshot := groupSnapshot(saved)
	groupCache.Set(saved.ID, snapshot)
	groupNameIndex.Set(saved.Name, saved.ID)
	*group = snapshot
	return nil
}

// GroupUpdate 更新分组配置和成员，并返回刷新后的分组。
func GroupUpdate(req *model.GroupUpdateRequest, ctx context.Context) (*model.Group, error) {
	if req == nil {
		return nil, fmt.Errorf("group update is required")
	}
	groupMutationMu.Lock()
	defer groupMutationMu.Unlock()

	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	oldName := oldGroup.Name

	var selectFields []string
	updates := model.Group{ID: req.ID}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("group name is required")
		}
		selectFields = append(selectFields, "name")
		updates.Name = name
	}
	if req.Enabled != nil {
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.Mode != nil {
		if err := validateGroupMode(*req.Mode); err != nil {
			return nil, err
		}
		selectFields = append(selectFields, "mode")
		updates.Mode = *req.Mode
	}
	if req.RelayConfig != nil {
		config := *req.RelayConfig
		model.NormalizeGroupRelayConfig(&config)
		selectFields = append(selectFields, "relay_config")
		updates.RelayConfig = config
	}

	newItems := make([]model.GroupItem, len(req.ItemsToAdd))
	for i, item := range req.ItemsToAdd {
		newItems[i] = groupItemFromAddRequest(req.ID, item)
		if err := normalizeAndValidateGroupItem(&newItems[i]); err != nil {
			return nil, fmt.Errorf("item %d: %w", i+1, err)
		}
	}

	var group model.Group
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(selectFields) > 0 {
			if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
				return fmt.Errorf("failed to update group: %w", err)
			}
		}

		if len(req.ItemsToDelete) > 0 {
			var deletedIDs []int
			if err := tx.Model(&model.GroupItem{}).
				Where("id IN ? AND group_id = ?", req.ItemsToDelete, req.ID).
				Pluck("id", &deletedIDs).Error; err != nil {
				return fmt.Errorf("failed to find deleted items: %w", err)
			}
			if len(deletedIDs) > 0 {
				if err := tx.Model(&model.Group{}).
					Where("id = ? AND active_item_id IN ?", req.ID, deletedIDs).
					Update("active_item_id", 0).Error; err != nil {
					return fmt.Errorf("failed to clear active item: %w", err)
				}
				if err := tx.Where("id IN ?", deletedIDs).Delete(&model.GroupItem{}).Error; err != nil {
					return fmt.Errorf("failed to delete items: %w", err)
				}
			}
		}

		for _, item := range req.ItemsToUpdate {
			fields := make(map[string]interface{}, 3)
			if item.Priority != 0 {
				fields["priority"] = item.Priority
			}
			if item.Disabled != nil {
				fields["disabled"] = *item.Disabled
			}
			if item.Weight != 0 {
				if item.Weight < 1 {
					return fmt.Errorf("group item %d weight must be positive", item.ID)
				}
				fields["weight"] = item.Weight
			}
			if len(fields) == 0 {
				continue
			}
			result := tx.Model(&model.GroupItem{}).
				Where("id = ? AND group_id = ?", item.ID, req.ID).
				Updates(fields)
			if result.Error != nil {
				return fmt.Errorf("failed to update item %d: %w", item.ID, result.Error)
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("group item %d not found", item.ID)
			}
		}

		if len(newItems) > 0 {
			if err := tx.Create(&newItems).Error; err != nil {
				return fmt.Errorf("failed to create items: %w", err)
			}
		}
		if err := validateGroupGraph(tx); err != nil {
			return err
		}
		if err := tx.Preload("Items").First(&group, req.ID).Error; err != nil {
			return fmt.Errorf("failed to load updated group: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortGroupItems(group.Items)
	snapshot := groupSnapshot(group)
	groupCache.Set(group.ID, snapshot)
	groupNameIndex.Set(group.Name, group.ID)
	if oldName != group.Name {
		groupNameIndex.Del(oldName)
	}
	return &snapshot, nil
}

// GroupActiveItemUpdate 更新或清空分组当前手动指定的成员。
func GroupActiveItemUpdate(groupID int, req *model.GroupActiveItemUpdateRequest, ctx context.Context) (*model.Group, error) {
	groupMutationMu.Lock()
	defer groupMutationMu.Unlock()

	group, ok := groupCache.Get(groupID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	itemID := 0
	if req != nil && req.ItemID != nil && *req.ItemID != 0 {
		itemID = *req.ItemID
		found := false
		for _, item := range group.Items {
			if item.ID != itemID || item.Disabled {
				continue
			}
			if normalizeGroupItemType(item.Type) == model.GroupItemTypeGroup {
				if item.TargetGroupID == nil {
					continue
				}
				target, err := GroupGetByID(*item.TargetGroupID)
				if err != nil || !target.Enabled {
					continue
				}
			}
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("enabled group item not found")
		}
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Group{}).Where("id = ?", groupID).Update("active_item_id", itemID).Error; err != nil {
		return nil, fmt.Errorf("failed to update active item: %w", err)
	}
	group.ActiveItemID = itemID
	snapshot := groupSnapshot(group)
	groupCache.Set(group.ID, snapshot)
	return &snapshot, nil
}

// GroupDel 删除未被其他分组引用的分组及其成员。
func GroupDel(id int, ctx context.Context) error {
	groupMutationMu.Lock()
	defer groupMutationMu.Unlock()

	group, ok := groupCache.Get(id)
	if !ok {
		return fmt.Errorf("group not found")
	}
	var references []model.GroupItem
	if err := db.GetDB().WithContext(ctx).
		Where("type = ? AND target_group_id = ?", model.GroupItemTypeGroup, id).
		Order("group_id ASC, id ASC").Find(&references).Error; err != nil {
		return fmt.Errorf("failed to find group references: %w", err)
	}
	if len(references) > 0 {
		names := make([]string, 0, len(references))
		for _, reference := range references {
			owner, err := GroupGetByID(reference.GroupID)
			if err == nil {
				names = append(names, owner.Name)
			} else {
				names = append(names, fmt.Sprintf("group %d", reference.GroupID))
			}
		}
		return fmt.Errorf("group is referenced by: %s", strings.Join(names, ", "))
	}
	if err := db.GetDB().WithContext(ctx).Delete(&model.Group{}, id).Error; err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}
	groupCache.Del(id)
	groupNameIndex.Del(group.Name)
	return nil
}

// groupRefreshCache 从数据库刷新完整分组缓存和名称索引。
func groupRefreshCache(ctx context.Context) error {
	groups := []model.Group{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Find(&groups).Error; err != nil {
		return err
	}
	groupCache.Clear()
	groupNameIndex.Clear()
	for _, group := range groups {
		sortGroupItems(group.Items)
		groupCache.Set(group.ID, groupSnapshot(group))
		groupNameIndex.Set(group.Name, group.ID)
	}
	return nil
}

// sortGroupItems 按优先级和主键生成稳定的成员顺序。
func sortGroupItems(items []model.GroupItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
}

// groupSnapshot 补齐渠道模型对象，并隔离缓存中的切片和可选主键指针。
func groupSnapshot(group model.Group) model.Group {
	group.Items = slices.Clone(group.Items)
	for i := range group.Items {
		item := &group.Items[i]
		item.Type = normalizeGroupItemType(item.Type)
		item.ChannelModelID = cloneIntPointer(item.ChannelModelID)
		item.TargetGroupID = cloneIntPointer(item.TargetGroupID)
		item.TargetGroup = nil
		item.ChannelModel = nil
		if item.Type != model.GroupItemTypeChannelModel || item.ChannelModelID == nil {
			continue
		}
		channelModel, err := ChannelModelGet(*item.ChannelModelID)
		if err == nil {
			item.ChannelModel = &channelModel
		}
	}
	return group
}

func validateGroupMode(mode model.GroupMode) error {
	switch mode {
	case model.GroupModeManual, model.GroupModeFailover, model.GroupModeRoundRobin, model.GroupModeRandom, model.GroupModeWeighted:
		return nil
	default:
		return fmt.Errorf("unsupported group mode: %s", mode)
	}
}

func normalizeGroupItemType(itemType model.GroupItemType) model.GroupItemType {
	if itemType == "" {
		return model.GroupItemTypeChannelModel
	}
	return itemType
}

func normalizeAndValidateGroupItem(item *model.GroupItem) error {
	item.Type = normalizeGroupItemType(item.Type)
	item.ChannelModel = nil
	item.TargetGroup = nil
	switch item.Type {
	case model.GroupItemTypeChannelModel:
		if item.ChannelModelID == nil || *item.ChannelModelID <= 0 {
			return fmt.Errorf("channel_model_id is required")
		}
		item.TargetGroupID = nil
	case model.GroupItemTypeGroup:
		if item.TargetGroupID == nil || *item.TargetGroupID <= 0 {
			return fmt.Errorf("target_group_id is required")
		}
		item.ChannelModelID = nil
	default:
		return fmt.Errorf("unsupported group item type: %s", item.Type)
	}
	return nil
}

func groupItemFromAddRequest(groupID int, req model.GroupItemAddRequest) model.GroupItem {
	itemType := normalizeGroupItemType(req.Type)
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	item := model.GroupItem{GroupID: groupID, Type: itemType, Priority: req.Priority, Weight: weight}
	if itemType == model.GroupItemTypeGroup {
		item.TargetGroupID = intPointer(req.TargetGroupID)
	} else {
		item.ChannelModelID = intPointer(req.ChannelModelID)
	}
	return item
}

func intPointer(value int) *int {
	if value == 0 {
		return nil
	}
	copy := value
	return &copy
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// validateGroupGraph 在事务内验证全部成员引用、环和最大嵌套深度。
func validateGroupGraph(tx *gorm.DB) error {
	var groups []model.Group
	if err := tx.Select("id").Find(&groups).Error; err != nil {
		return fmt.Errorf("failed to load groups for validation: %w", err)
	}
	groupIDs := make(map[int]struct{}, len(groups))
	for _, group := range groups {
		groupIDs[group.ID] = struct{}{}
	}

	var items []model.GroupItem
	if err := tx.Select("id", "group_id", "type", "channel_model_id", "target_group_id").Find(&items).Error; err != nil {
		return fmt.Errorf("failed to load group items for validation: %w", err)
	}
	channelModelIDs := make(map[int]struct{})
	var channelModels []model.ChannelModel
	if err := tx.Select("id").Find(&channelModels).Error; err != nil {
		return fmt.Errorf("failed to load channel models for validation: %w", err)
	}
	for _, channelModel := range channelModels {
		channelModelIDs[channelModel.ID] = struct{}{}
	}

	edges := make(map[int][]int, len(groups))
	for _, item := range items {
		item.Type = normalizeGroupItemType(item.Type)
		if _, ok := groupIDs[item.GroupID]; !ok {
			return fmt.Errorf("group item %d references missing owner group %d", item.ID, item.GroupID)
		}
		switch item.Type {
		case model.GroupItemTypeChannelModel:
			if item.ChannelModelID == nil {
				return fmt.Errorf("group item %d is missing channel_model_id", item.ID)
			}
			if _, ok := channelModelIDs[*item.ChannelModelID]; !ok {
				return fmt.Errorf("group item %d references missing channel model %d", item.ID, *item.ChannelModelID)
			}
		case model.GroupItemTypeGroup:
			if item.TargetGroupID == nil {
				return fmt.Errorf("group item %d is missing target_group_id", item.ID)
			}
			if _, ok := groupIDs[*item.TargetGroupID]; !ok {
				return fmt.Errorf("group item %d references missing group %d", item.ID, *item.TargetGroupID)
			}
			edges[item.GroupID] = append(edges[item.GroupID], *item.TargetGroupID)
		default:
			return fmt.Errorf("group item %d has unsupported type %q", item.ID, item.Type)
		}
	}

	var walk func(int, int, map[int]struct{}) error
	walk = func(groupID, depth int, path map[int]struct{}) error {
		if depth > MaxGroupNestDepth {
			return fmt.Errorf("group nesting depth exceeded (max %d)", MaxGroupNestDepth)
		}
		if _, exists := path[groupID]; exists {
			return fmt.Errorf("circular group reference detected at group %d", groupID)
		}
		nextPath := make(map[int]struct{}, len(path)+1)
		for id := range path {
			nextPath[id] = struct{}{}
		}
		nextPath[groupID] = struct{}{}
		for _, targetID := range edges[groupID] {
			if err := walk(targetID, depth+1, nextPath); err != nil {
				return err
			}
		}
		return nil
	}
	for groupID := range groupIDs {
		if err := walk(groupID, 0, map[int]struct{}{}); err != nil {
			return err
		}
	}
	return nil
}
