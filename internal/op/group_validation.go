package op

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateGroupItemFields(tx *gorm.DB, ownerGroupID int, itemType string, channelID, targetGroupID int, modelName string) error {
	itemType = normalizeGroupItemType(itemType)
	modelName = strings.TrimSpace(modelName)

	switch itemType {
	case model.GroupItemTypeChannel:
		return validateChannelGroupItem(tx, channelID, targetGroupID, modelName)
	case model.GroupItemTypeGroup:
		return validateNestedGroupItem(tx, ownerGroupID, channelID, targetGroupID, modelName)
	default:
		return fmt.Errorf("unsupported group item type: %s", itemType)
	}
}

func validateChannelGroupItem(tx *gorm.DB, channelID, targetGroupID int, modelName string) error {
	if channelID <= 0 || modelName == "" {
		return fmt.Errorf("channel group item requires channel_id and model_name")
	}
	if targetGroupID != 0 {
		return fmt.Errorf("channel group item cannot set target_group_id")
	}
	if tx == nil {
		return fmt.Errorf("db transaction is required")
	}
	var count int64
	if err := tx.Model(&model.Channel{}).Where("id = ?", channelID).Count(&count).Error; err != nil {
		return fmt.Errorf("failed to verify channel: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: channel not found", ErrNotFound)
	}
	return nil
}

func validateNestedGroupItem(tx *gorm.DB, ownerGroupID, channelID, targetGroupID int, modelName string) error {
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
	if err := ensureGroupExists(tx, ownerGroupID, "owner"); err != nil {
		return err
	}
	if err := ensureGroupExists(tx, targetGroupID, "target"); err != nil {
		return err
	}
	graph, err := buildGroupGraphFromDB(tx, ownerGroupID, targetGroupID)
	if err != nil {
		return err
	}
	if detectCycleInGraph(graph, ownerGroupID) {
		return fmt.Errorf("group nesting creates circular reference")
	}
	maxDepth := graphMaxDepth(graph)
	if maxDepth > MaxGroupNestDepth {
		return fmt.Errorf("group nesting depth exceeded (max %d, found %d)", MaxGroupNestDepth, maxDepth)
	}
	return nil
}

func ensureGroupExists(tx *gorm.DB, groupID int, label string) error {
	var group model.Group
	if err := tx.Select("id").First(&group, groupID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("%w: %s group not found", ErrNotFound, label)
		}
		return fmt.Errorf("failed to verify %s group: %w", label, err)
	}
	return nil
}

func graphMaxDepth(graph groupGraph) int {
	maxDepth := 0
	for node := range graph {
		if depth := calculateMaxDepth(graph, node); depth > maxDepth {
			maxDepth = depth
		}
	}
	return maxDepth
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
