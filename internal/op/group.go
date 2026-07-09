package op

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var groupCache = cache.New[int, model.Group](16)
var groupMap = cache.New[string, model.Group](16)
var groupService = NewGroupService(groupCache, groupMap)

// MaxGroupNestDepth 限制嵌套 group 的最大层级，写入校验与 relay 运行时守卫共用。
const MaxGroupNestDepth = 3

type GroupService struct {
	groups      cache.Cache[int, model.Group]
	groupsByKey cache.Cache[string, model.Group]
}

func NewGroupService(groups cache.Cache[int, model.Group], groupsByKey cache.Cache[string, model.Group]) *GroupService {
	if groups == nil {
		groups = cache.New[int, model.Group](16)
	}
	if groupsByKey == nil {
		groupsByKey = cache.New[string, model.Group](16)
	}
	return &GroupService{
		groups:      groups,
		groupsByKey: groupsByKey,
	}
}

func GroupList(ctx context.Context) ([]model.Group, error) {
	return groupService.List(ctx)
}

func (s *GroupService) List(ctx context.Context) ([]model.Group, error) {
	groups := make([]model.Group, 0, s.groups.Len())
	for _, group := range s.groups.GetAll() {
		groups = append(groups, group)
	}
	return groups, nil
}

func GroupListModel(ctx context.Context) ([]string, error) {
	return groupService.ListModel(ctx)
}

func (s *GroupService) ListModel(ctx context.Context) ([]string, error) {
	models := []string{}
	for _, group := range s.groups.GetAll() {
		// 临时禁用的分组不对外暴露为可用模型。
		if !group.Enabled {
			continue
		}
		models = append(models, group.Name)
	}
	return models, nil
}

func GroupGet(id int, ctx context.Context) (*model.Group, error) {
	return groupService.Get(id, ctx)
}

func (s *GroupService) Get(id int, ctx context.Context) (*model.Group, error) {
	group, ok := s.groups.Get(id)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	return &group, nil
}

func GroupGetEnabledMap(name string, ctx context.Context) (model.Group, error) {
	return groupService.GetEnabledMap(name, ctx)
}

func (s *GroupService) GetEnabledMap(name string, ctx context.Context) (model.Group, error) {
	group, ok := s.groupsByKey.Get(name)
	if !ok {
		// 尝试后备查找：去掉常见后缀
		fallbackName := stripModelSuffix(name)
		if fallbackName != name {
			group, ok = s.groupsByKey.Get(fallbackName)
			if ok {
				// 找到后备模型，继续处理
				goto processGroup
			}
		}
		return model.Group{}, fmt.Errorf("group not found")
	}

processGroup:
	return expandEnabledGroup(group)
}

func GroupGetEnabledTree(name string, ctx context.Context) (model.Group, error) {
	return groupService.GetEnabledTree(name, ctx)
}

func (s *GroupService) GetEnabledTree(name string, ctx context.Context) (model.Group, error) {
	group, ok := s.groupsByKey.Get(name)
	if !ok {
		fallbackName := stripModelSuffix(name)
		if fallbackName != name {
			group, ok = s.groupsByKey.Get(fallbackName)
			if ok {
				goto processGroup
			}
		}
		return model.Group{}, fmt.Errorf("group not found")
	}

processGroup:
	visited := map[int]struct{}{group.ID: {}}
	return filterEnabledGroupTree(group, 0, visited)
}

// GroupGetEnabledByID 根据分组 ID 获取启用的分组，并递归展开嵌套分组成员
func GroupGetEnabledByID(id int, ctx context.Context) (*model.Group, error) {
	return groupService.GetEnabledByID(id, ctx)
}

func (s *GroupService) GetEnabledByID(id int, ctx context.Context) (*model.Group, error) {
	group, ok := s.groups.Get(id)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	expanded, err := expandEnabledGroup(group)
	if err != nil {
		return nil, err
	}
	return &expanded, nil
}

func GroupGetEnabledTreeByID(id int, ctx context.Context) (*model.Group, error) {
	return groupService.GetEnabledTreeByID(id, ctx)
}

func (s *GroupService) GetEnabledTreeByID(id int, ctx context.Context) (*model.Group, error) {
	group, ok := s.groups.Get(id)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	visited := map[int]struct{}{group.ID: {}}
	expanded, err := filterEnabledGroupTree(group, 0, visited)
	if err != nil {
		return nil, err
	}
	return &expanded, nil
}

func expandEnabledGroup(group model.Group) (model.Group, error) {
	if !group.Enabled {
		group.Items = nil
		return group, nil
	}
	visited := map[int]struct{}{group.ID: {}}
	items, err := expandGroupItems(group, 0, visited)
	if err != nil {
		return model.Group{}, err
	}
	group.Items = items
	return group, nil
}

func filterEnabledGroupTree(group model.Group, depth int, visited map[int]struct{}) (model.Group, error) {
	if depth > MaxGroupNestDepth {
		return model.Group{}, fmt.Errorf("group %d: nesting depth exceeded (max %d)", group.ID, MaxGroupNestDepth)
	}
	if !group.Enabled || len(group.Items) == 0 {
		group.Items = nil
		return group, nil
	}

	items := make([]model.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		if item.Disabled {
			continue
		}

		item.Type = normalizeGroupItemType(item.Type)
		switch item.Type {
		case model.GroupItemTypeChannel:
			channel, ok := channelCache.Get(item.ChannelID)
			if !ok || !channel.Enabled {
				continue
			}
			items = append(items, item)

		case model.GroupItemTypeGroup:
			if item.TargetGroupID <= 0 {
				continue
			}
			if _, ok := visited[item.TargetGroupID]; ok {
				return model.Group{}, fmt.Errorf("group %d: circular reference detected (target %d)", group.ID, item.TargetGroupID)
			}
			targetGroup, ok := groupCache.Get(item.TargetGroupID)
			if !ok || !targetGroup.Enabled {
				continue
			}
			nextVisited := cloneIntSet(visited)
			nextVisited[item.TargetGroupID] = struct{}{}
			filteredTarget, err := filterEnabledGroupTree(targetGroup, depth+1, nextVisited)
			if err != nil {
				return model.Group{}, err
			}
			if len(filteredTarget.Items) == 0 {
				continue
			}
			items = append(items, item)
		}
	}

	group.Items = items
	return group, nil
}

func expandGroupItems(group model.Group, depth int, visited map[int]struct{}) ([]model.GroupItem, error) {
	if depth > MaxGroupNestDepth {
		return nil, fmt.Errorf("group %d: nesting depth exceeded (max %d)", group.ID, MaxGroupNestDepth)
	}
	if !group.Enabled || len(group.Items) == 0 {
		return nil, nil
	}

	out := make([]model.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		if item.Disabled {
			continue
		}

		itemType := normalizeGroupItemType(item.Type)
		if itemType == model.GroupItemTypeChannel {
			channel, ok := channelCache.Get(item.ChannelID)
			if !ok || !channel.Enabled {
				continue
			}
			out = append(out, item)
			continue
		}

		if itemType != model.GroupItemTypeGroup {
			continue
		}

		if item.TargetGroupID <= 0 {
			continue
		}

		if _, ok := visited[item.TargetGroupID]; ok {
			return nil, fmt.Errorf("group %d: circular reference detected (target %d)", group.ID, item.TargetGroupID)
		}

		targetGroup, ok := groupCache.Get(item.TargetGroupID)
		if !ok {
			continue
		}

		if !targetGroup.Enabled {
			continue
		}

		nextVisited := cloneIntSet(visited)
		nextVisited[item.TargetGroupID] = struct{}{}

		childItems, err := expandGroupItems(targetGroup, depth+1, nextVisited)
		if err != nil {
			return nil, err
		}

		out = append(out, childItems...)
	}

	return out, nil
}

func normalizeGroupItemType(itemType string) string {
	itemType = strings.TrimSpace(itemType)
	if itemType == "" {
		return model.GroupItemTypeChannel
	}
	return itemType
}

func cloneIntSet(src map[int]struct{}) map[int]struct{} {
	dst := make(map[int]struct{}, len(src)+1)
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}

// filterEnabledGroupItems 已被 expandEnabledGroup 替代，保留用于兼容性
func filterEnabledGroupItems(group model.Group) model.Group {
	expanded, err := expandEnabledGroup(group)
	if err != nil {
		group.Items = nil
		return group
	}
	return expanded
}

// stripModelSuffix 去除模型名的常见后缀，使带后缀的请求回退到基础分组。
// 循环剥离：支持组合后缀（如 "gpt-5.5-thinking-openai-compact" → "gpt-5.5"）。
// 同时处理 [xxx] 形式的方括号后缀（如 "claude-opus-4-8[1m]" → "claude-opus-4-8"）。
func stripModelSuffix(name string) string {
	// 连字符后缀：协议变体 + 能力变体。按长度由长到短排列，确保组合后缀（-openai-compact）
	// 优先于其子串（-openai、-compact）被整体匹配。
	suffixes := []string{
		"-openai-compact",
		"-anthropic",
		"-openai",
		"-compact",
		"-gemini",
		"-thinking",
		"-reasoning",
		"-search",
		"-web",
		"-1m",
	}
	for {
		original := name
		// 先剥方括号后缀，如 [1m]、[thinking]：仅当方括号在末尾且前面还有基础名时剥离。
		if idx := lastIndexByte(name, '['); idx > 0 && name[len(name)-1] == ']' {
			name = name[:idx]
		}
		for _, suffix := range suffixes {
			if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
				name = name[:len(name)-len(suffix)]
				break
			}
		}
		// 一轮下来没有任何剥离，说明已到基础名，停止。
		if name == original {
			break
		}
	}
	return name
}

// lastIndexByte 返回 b 在 s 中最后出现的下标，不存在返回 -1。
func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func GroupCreate(group *model.Group, ctx context.Context) error {
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
			return err
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
	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("group not found")
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

	var selectFields []string
	updates := model.Group{ID: req.ID}

	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Enabled != nil {
		// Select 显式包含 enabled 列，确保能写入 false（GORM Updates 默认跳过零值）。
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.Mode != nil {
		selectFields = append(selectFields, "mode")
		updates.Mode = *req.Mode
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = *req.MatchRegex
	}
	if req.FirstTokenTimeOut != nil {
		selectFields = append(selectFields, "first_token_time_out")
		updates.FirstTokenTimeOut = *req.FirstTokenTimeOut
	}
	if req.SessionKeepTime != nil {
		selectFields = append(selectFields, "session_keep_time")
		updates.SessionKeepTime = *req.SessionKeepTime
	}

	if len(selectFields) > 0 {
		if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update group: %w", err)
		}
	}

	// 删除 items
	if len(req.ItemsToDelete) > 0 {
		if err := tx.Where("id IN ? AND group_id = ?", req.ItemsToDelete, req.ID).Delete(&model.GroupItem{}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to delete items: %w", err)
		}
	}

	// 批量更新 items
	if len(req.ItemsToUpdate) > 0 {
		ids := make([]int, len(req.ItemsToUpdate))
		priorityCase := "CASE id"
		weightCase := "CASE id"
		disabledCase := "CASE id"
		hasDisabledChange := false
		for i, item := range req.ItemsToUpdate {
			ids[i] = item.ID
			priorityCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Priority)
			weightCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Weight)
			// disabled 为可选项：仅对显式变更的成员生成 WHEN 分支，其余走 ELSE 保留原值。
			// 使用 true/false 字面量以兼容 Postgres 布尔列的严格类型校验。
			if item.Disabled != nil {
				hasDisabledChange = true
				disabledCase += fmt.Sprintf(" WHEN %d THEN %t", item.ID, *item.Disabled)
			}
		}
		priorityCase += " END"
		weightCase += " END"
		disabledCase += " ELSE disabled END"

		updateColumns := map[string]interface{}{
			"priority": gorm.Expr(priorityCase),
			"weight":   gorm.Expr(weightCase),
		}
		if hasDisabledChange {
			updateColumns["disabled"] = gorm.Expr(disabledCase)
		}

		if err := tx.Model(&model.GroupItem{}).
			Where("id IN ? AND group_id = ?", ids, req.ID).
			Updates(updateColumns).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update items: %w", err)
		}
	}

	// 批量新增 items
	if len(req.ItemsToAdd) > 0 {
		newItems := make([]model.GroupItem, len(req.ItemsToAdd))
		for i, item := range req.ItemsToAdd {
			itemType := normalizeGroupItemType(item.Type)
			modelName := strings.TrimSpace(item.ModelName)
			if err := validateGroupItemFields(tx, req.ID, itemType, item.ChannelID, item.TargetGroupID, modelName); err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("invalid group item: %w", err)
			}

			newItems[i] = model.GroupItem{
				GroupID:       req.ID,
				Type:          itemType,
				ChannelID:     item.ChannelID,
				TargetGroupID: item.TargetGroupID,
				ModelName:     modelName,
				Priority:      item.Priority,
				Weight:        item.Weight,
			}
		}
		if err := tx.Create(&newItems).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create items: %w", err)
		}
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
		return fmt.Errorf("group not found")
	}

	refs, err := findReferencingGroupItemsTx(tx, id)
	if err != nil {
		tx.Rollback()
		return err
	}
	if len(refs) > 0 {
		tx.Rollback()
		return fmt.Errorf("cannot delete group: referenced by groups: %v", refs)
	}

	if err := tx.Where("group_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := tx.Delete(&model.Group{}, id).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	groupCache.Del(id)
	groupMap.Del(group.Name)
	return nil
}

func GroupItemAdd(item *model.GroupItem, ctx context.Context) error {
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

	var group model.Group
	if err := tx.Select("id").First(&group, item.GroupID).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("group not found")
	}

	item.Type = normalizeGroupItemType(item.Type)
	item.ModelName = strings.TrimSpace(item.ModelName)
	if err := validateGroupItemFields(tx, item.GroupID, item.Type, item.ChannelID, item.TargetGroupID, item.ModelName); err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Create(item).Error; err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
}

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

func GroupItemUpdate(item *model.GroupItem, ctx context.Context) error {
	if err := db.GetDB().WithContext(ctx).Model(item).
		Select("ModelName", "Priority", "Weight").
		Updates(item).Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
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

func GroupRefreshCacheByID(id int, ctx context.Context) error {
	return groupRefreshCacheByID(id, ctx)
}

func GroupItemDel(id int, ctx context.Context) error {
	var item model.GroupItem
	if err := db.GetDB().WithContext(ctx).First(&item, id).Error; err != nil {
		return fmt.Errorf("group item not found")
	}

	if err := db.GetDB().WithContext(ctx).Delete(&item).Error; err != nil {
		return err
	}

	return groupRefreshCacheByID(item.GroupID, ctx)
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

func validateGroupItemFields(tx *gorm.DB, ownerGroupID int, itemType string, channelID, targetGroupID int, modelName string) error {
	itemType = normalizeGroupItemType(itemType)
	modelName = strings.TrimSpace(modelName)

	switch itemType {
	case model.GroupItemTypeChannel:
		if channelID <= 0 || modelName == "" {
			return fmt.Errorf("channel group item requires channel_id and model_name")
		}
		if targetGroupID != 0 {
			return fmt.Errorf("channel group item cannot set target_group_id")
		}
	case model.GroupItemTypeGroup:
		if targetGroupID <= 0 {
			return fmt.Errorf("nested group item requires target_group_id")
		}
		if channelID != 0 || modelName != "" {
			return fmt.Errorf("nested group item cannot set channel_id or model_name")
		}
		if ownerGroupID == targetGroupID {
			return fmt.Errorf("group cannot reference itself")
		}

		if err := lockGroupRowsForNestingTx(tx); err != nil {
			return err
		}

		var ownerGroup model.Group
		if err := tx.Select("id").First(&ownerGroup, ownerGroupID).Error; err != nil {
			return fmt.Errorf("owner group not found")
		}

		var targetGroup model.Group
		if err := tx.Select("id").First(&targetGroup, targetGroupID).Error; err != nil {
			return fmt.Errorf("target group not found")
		}

		graph, err := buildGroupGraphFromDB(tx, ownerGroupID, targetGroupID)
		if err != nil {
			return err
		}
		if detectCycleInGraph(graph, ownerGroupID) {
			return fmt.Errorf("group nesting creates circular reference")
		}

		// 检查从所有根节点（无入边的节点）到图中任意节点的最大深度
		// 这样可以捕获祖先链 + 当前新增边导致的深度超限
		maxDepthInGraph := 0
		for node := range graph {
			// 对每个节点计算其作为起点的最大深度
			if nodeDepth := calculateMaxDepth(graph, node); nodeDepth > maxDepthInGraph {
				maxDepthInGraph = nodeDepth
			}
		}
		if maxDepthInGraph > MaxGroupNestDepth {
			return fmt.Errorf("group nesting depth exceeded (max %d, found %d)", MaxGroupNestDepth, maxDepthInGraph)
		}
	default:
		return fmt.Errorf("unsupported group item type: %s", itemType)
	}

	return nil
}

func findReferencingGroupItemsTx(tx *gorm.DB, targetGroupID int) ([]string, error) {
	var rows []struct {
		GroupName string `gorm:"column:group_name"`
		ItemID    int    `gorm:"column:item_id"`
	}
	if err := tx.Table("group_items").
		Select("groups.name AS group_name, group_items.id AS item_id").
		Joins("JOIN groups ON groups.id = group_items.group_id").
		Where("group_items.type = ? AND group_items.target_group_id = ?", model.GroupItemTypeGroup, targetGroupID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to find referencing group items: %w", err)
	}

	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, fmt.Sprintf("%s(item:%d)", row.GroupName, row.ItemID))
	}
	sort.Strings(refs)
	return refs, nil
}

type groupGraph map[int][]int

func containsNestedGroupItemAdd(items []model.GroupItemAddRequest) bool {
	for _, item := range items {
		if normalizeGroupItemType(item.Type) == model.GroupItemTypeGroup {
			return true
		}
	}
	return false
}

func lockGroupRowsForNestingTx(tx *gorm.DB) error {
	if tx == nil {
		return fmt.Errorf("db transaction is required")
	}
	var ids []int
	if err := tx.Model(&model.Group{}).
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Order("id ASC").
		Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("failed to lock groups for nesting: %w", err)
	}
	return nil
}

func buildGroupGraphFromDB(tx *gorm.DB, ownerGroupID int, newTargetGroupID int) (groupGraph, error) {
	if tx == nil {
		return nil, fmt.Errorf("db transaction is required")
	}
	var items []model.GroupItem
	if err := tx.Model(&model.GroupItem{}).
		Select("group_id", "target_group_id").
		Where("type = ? AND target_group_id > 0", model.GroupItemTypeGroup).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("failed to build group graph: %w", err)
	}
	graph := make(groupGraph)
	for _, item := range items {
		if item.GroupID <= 0 || item.TargetGroupID <= 0 {
			continue
		}
		graph[item.GroupID] = append(graph[item.GroupID], item.TargetGroupID)
		if _, ok := graph[item.TargetGroupID]; !ok {
			graph[item.TargetGroupID] = nil
		}
	}
	if ownerGroupID > 0 && newTargetGroupID > 0 {
		graph[ownerGroupID] = append(graph[ownerGroupID], newTargetGroupID)
		if _, ok := graph[newTargetGroupID]; !ok {
			graph[newTargetGroupID] = nil
		}
	}
	return graph, nil
}

func detectCycleInGraph(graph groupGraph, startNode int) bool {
	visiting := map[int]struct{}{}
	visited := map[int]struct{}{}
	var visit func(int) bool
	visit = func(node int) bool {
		if _, ok := visiting[node]; ok {
			return true
		}
		if _, ok := visited[node]; ok {
			return false
		}
		visiting[node] = struct{}{}
		for _, next := range graph[node] {
			if visit(next) {
				return true
			}
		}
		delete(visiting, node)
		visited[node] = struct{}{}
		return false
	}
	return visit(startNode)
}

func calculateMaxDepth(graph groupGraph, startNode int) int {
	var visit func(node, depth int, path map[int]struct{}) int
	visit = func(node, depth int, path map[int]struct{}) int {
		if _, ok := path[node]; ok {
			return depth
		}
		path[node] = struct{}{}
		maxDepth := depth
		for _, next := range graph[node] {
			if childDepth := visit(next, depth+1, path); childDepth > maxDepth {
				maxDepth = childDepth
			}
		}
		delete(path, node)
		return maxDepth
	}
	return visit(startNode, 0, map[int]struct{}{})
}
