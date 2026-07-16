package op

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
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
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
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
	sort.Strings(models)
	return models, nil
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
		include, normalized, err := enabledGroupItem(group.ID, item, depth, visited)
		if err != nil {
			return model.Group{}, err
		}
		if include {
			items = append(items, normalized)
		}
	}

	group.Items = items
	return group, nil
}

func enabledGroupItem(ownerID int, item model.GroupItem, depth int, visited map[int]struct{}) (bool, model.GroupItem, error) {
	if item.Disabled {
		return false, item, nil
	}
	item.Type = normalizeGroupItemType(item.Type)
	if item.Type == model.GroupItemTypeChannel {
		channel, ok := channelCache.Get(item.ChannelID)
		return ok && channel.Enabled, item, nil
	}
	if item.Type != model.GroupItemTypeGroup || item.TargetGroupID <= 0 {
		return false, item, nil
	}
	if _, ok := visited[item.TargetGroupID]; ok {
		return false, item, fmt.Errorf("group %d: circular reference detected (target %d)", ownerID, item.TargetGroupID)
	}
	targetGroup, ok := groupCache.Get(item.TargetGroupID)
	if !ok || !targetGroup.Enabled {
		return false, item, nil
	}
	nextVisited := cloneIntSet(visited)
	nextVisited[item.TargetGroupID] = struct{}{}
	filteredTarget, err := filterEnabledGroupTree(targetGroup, depth+1, nextVisited)
	return err == nil && len(filteredTarget.Items) > 0, item, err
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
