package op

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

func initTestDB(t *testing.T) {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "octopus.db"), false); err != nil {
		t.Fatalf("init test db: %v", err)
	}
	groupCache.Clear()
	groupMap.Clear()
	channelCache.Clear()
	t.Cleanup(func() {
		_ = db.Close()
		groupCache.Clear()
		groupMap.Clear()
		channelCache.Clear()
	})
}

func TestStripModelSuffix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "remove -openai-compact",
			input:    "gpt-5.5-openai-compact",
			expected: "gpt-5.5",
		},
		{
			name:     "remove -openai",
			input:    "gpt-4-openai",
			expected: "gpt-4",
		},
		{
			name:     "remove -compact",
			input:    "claude-opus-compact",
			expected: "claude-opus",
		},
		{
			name:     "remove -anthropic",
			input:    "claude-3-5-sonnet-anthropic",
			expected: "claude-3-5-sonnet",
		},
		{
			name:     "remove -gemini",
			input:    "gemini-pro-gemini",
			expected: "gemini-pro",
		},
		{
			name:     "remove -thinking",
			input:    "claude-opus-4-8-thinking",
			expected: "claude-opus-4-8",
		},
		{
			name:     "remove -1m",
			input:    "claude-opus-4-8-1m",
			expected: "claude-opus-4-8",
		},
		{
			name:     "remove [1m] bracket suffix",
			input:    "claude-opus-4-8[1m]",
			expected: "claude-opus-4-8",
		},
		{
			name:     "remove combined -thinking-openai-compact",
			input:    "gpt-5.5-thinking-openai-compact",
			expected: "gpt-5.5",
		},
		{
			name:     "remove combined -thinking[1m]",
			input:    "claude-opus-4-8-thinking[1m]",
			expected: "claude-opus-4-8",
		},
		{
			name:     "remove -reasoning",
			input:    "grok-4.3-reasoning",
			expected: "grok-4.3",
		},
		{
			name:     "no suffix to remove",
			input:    "gpt-4-turbo",
			expected: "gpt-4-turbo",
		},
		{
			name:     "suffix in middle not removed",
			input:    "gpt-openai-4",
			expected: "gpt-openai-4",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripModelSuffix(tt.input)
			if result != tt.expected {
				t.Errorf("stripModelSuffix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGroupServiceReadMethodsUseInjectedCache(t *testing.T) {
	groups := cache.New[int, model.Group](1)
	groupsByKey := cache.New[string, model.Group](1)
	enabled := model.Group{ID: 1, Name: "enabled", Enabled: true}
	disabled := model.Group{ID: 2, Name: "disabled", Enabled: false}
	groups.Set(enabled.ID, enabled)
	groups.Set(disabled.ID, disabled)
	groupsByKey.Set(enabled.Name, enabled)
	groupsByKey.Set(disabled.Name, disabled)

	service := NewGroupService(groups, groupsByKey)
	got, err := service.Get(enabled.ID, context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Name != enabled.Name {
		t.Fatalf("Get name = %q, want %q", got.Name, enabled.Name)
	}
	got.Name = "mutated"
	again, err := service.Get(enabled.ID, context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if again.Name != enabled.Name {
		t.Fatal("Get should return a copy of cached group")
	}

	models, err := service.ListModel(context.Background())
	if err != nil {
		t.Fatalf("ListModel returned error: %v", err)
	}
	if len(models) != 1 || models[0] != enabled.Name {
		t.Fatalf("ListModel = %#v, want only enabled group", models)
	}

	expanded, err := service.GetEnabledMap(disabled.Name, context.Background())
	if err != nil {
		t.Fatalf("GetEnabledMap returned error: %v", err)
	}
	if len(expanded.Items) != 0 {
		t.Fatal("disabled group should return no enabled items")
	}
}

func TestGroupGetEnabledMapDisabledGroup(t *testing.T) {
	// 构造一个禁用分组，确认返回空 Items，符合"无可用通道"语义。
	testGroup := model.Group{
		ID:      99,
		Name:    "test-disabled-group",
		Enabled: false,
		Items: []model.GroupItem{
			{ID: 1, ChannelID: 10, ModelName: "test-model", Priority: 1},
		},
	}
	groupCache.Set(testGroup.ID, testGroup)
	groupMap.Set(testGroup.Name, testGroup)
	defer func() {
		groupCache.Del(testGroup.ID)
		groupMap.Del(testGroup.Name)
	}()

	result, err := GroupGetEnabledMap(testGroup.Name, context.Background())
	if err != nil {
		t.Fatalf("GroupGetEnabledMap 返回错误: %v", err)
	}
	if result.Enabled {
		t.Errorf("返回的 group.Enabled = true, 期望 false")
	}
	if len(result.Items) != 0 {
		t.Errorf("禁用分组返回 Items 长度 %d，期望 0（空候选让 relay 走无可用通道）", len(result.Items))
	}
}

func TestGroupGetEnabledTreeKeepsNestedGroupBoundary(t *testing.T) {
	parent := model.Group{
		ID:      201,
		Name:    "opus",
		Enabled: true,
		Mode:    model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 1, GroupID: 201, Type: model.GroupItemTypeChannel, ChannelID: 301, ModelName: "claude-opus", Priority: 1},
			{ID: 2, GroupID: 201, Type: model.GroupItemTypeGroup, TargetGroupID: 202, Priority: 2},
		},
	}
	child := model.Group{
		ID:      202,
		Name:    "gpt",
		Enabled: true,
		Mode:    model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ID: 3, GroupID: 202, Type: model.GroupItemTypeChannel, ChannelID: 302, ModelName: "gpt-5", Priority: 2},
			{ID: 4, GroupID: 202, Type: model.GroupItemTypeChannel, ChannelID: 303, ModelName: "gpt-4.1", Priority: 1},
		},
	}
	channelCache.Set(301, model.Channel{ID: 301, Enabled: true})
	channelCache.Set(302, model.Channel{ID: 302, Enabled: true})
	channelCache.Set(303, model.Channel{ID: 303, Enabled: true})
	groupCache.Set(parent.ID, parent)
	groupCache.Set(child.ID, child)
	groupMap.Set(parent.Name, parent)
	defer func() {
		channelCache.Del(301)
		channelCache.Del(302)
		channelCache.Del(303)
		groupCache.Del(parent.ID)
		groupCache.Del(child.ID)
		groupMap.Del(parent.Name)
	}()

	tree, err := GroupGetEnabledTree(parent.Name, context.Background())
	if err != nil {
		t.Fatalf("GroupGetEnabledTree 返回错误: %v", err)
	}
	if len(tree.Items) != 2 {
		t.Fatalf("父分组 items 数量 = %d, 期望保留直连渠道和嵌套分组", len(tree.Items))
	}
	if tree.Items[0].Type != model.GroupItemTypeChannel || tree.Items[0].ChannelID != 301 {
		t.Fatalf("第一个 item = %+v, 期望父级直连渠道", tree.Items[0])
	}
	if tree.Items[1].Type != model.GroupItemTypeGroup || tree.Items[1].TargetGroupID != child.ID {
		t.Fatalf("第二个 item = %+v, 期望保留嵌套分组引用", tree.Items[1])
	}

	flat, err := GroupGetEnabledMap(parent.Name, context.Background())
	if err != nil {
		t.Fatalf("GroupGetEnabledMap 返回错误: %v", err)
	}
	if len(flat.Items) != 3 || flat.Items[1].Type == model.GroupItemTypeGroup {
		t.Fatalf("旧 flat 查询应继续展开子分组, got %+v", flat.Items)
	}
}

func TestGroupItemPruneByChannelModelsDeletesStaleModels(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	group := model.Group{ID: 301, Name: "prune-group", Enabled: true, Mode: model.GroupModeFailover}
	if err := db.GetDB().WithContext(ctx).Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	items := []model.GroupItem{
		{ID: 1001, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 501, ModelName: "GPT-4", Priority: 1},
		{ID: 1002, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 501, ModelName: "old-model", Priority: 2},
		{ID: 1003, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 502, ModelName: "old-model", Priority: 3},
		{ID: 1004, GroupID: group.ID, Type: model.GroupItemTypeGroup, TargetGroupID: 302, Priority: 4},
	}
	if err := db.GetDB().WithContext(ctx).Create(&items).Error; err != nil {
		t.Fatalf("create group items: %v", err)
	}
	group.Items = items
	groupCache.Set(group.ID, group)
	groupMap.Set(group.Name, group)

	if err := GroupItemPruneByChannelModels(501, []string{"gpt-4"}, ctx); err != nil {
		t.Fatalf("GroupItemPruneByChannelModels 返回错误: %v", err)
	}

	remaining, err := GroupItemList(group.ID, ctx)
	if err != nil {
		t.Fatalf("list group items: %v", err)
	}
	ids := map[int]bool{}
	for _, item := range remaining {
		ids[item.ID] = true
	}
	if !ids[1001] {
		t.Fatal("大小写不同但仍存在于渠道模型列表的分组项不应被删除")
	}
	if ids[1002] {
		t.Fatal("同渠道已下架模型的分组项应被删除")
	}
	if !ids[1003] {
		t.Fatal("其它渠道的同名模型分组项不应被删除")
	}
	if !ids[1004] {
		t.Fatal("嵌套分组项不应被删除")
	}

	refreshed, ok := groupCache.Get(group.ID)
	if !ok {
		t.Fatal("清理后应刷新分组缓存")
	}
	for _, item := range refreshed.Items {
		if item.ID == 1002 {
			t.Fatal("刷新后的分组缓存不应包含已删除的旧模型项")
		}
	}
}

func TestGroupItemPruneByChannelModelsDeletesAllWhenModelListEmpty(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	group := model.Group{ID: 401, Name: "empty-model-group", Enabled: true, Mode: model.GroupModeFailover}
	if err := db.GetDB().WithContext(ctx).Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	items := []model.GroupItem{
		{ID: 2001, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 601, ModelName: "old-a", Priority: 1},
		{ID: 2002, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 601, ModelName: "old-b", Priority: 2},
	}
	if err := db.GetDB().WithContext(ctx).Create(&items).Error; err != nil {
		t.Fatalf("create group items: %v", err)
	}
	group.Items = items
	groupCache.Set(group.ID, group)
	groupMap.Set(group.Name, group)

	if err := GroupItemPruneByChannelModels(601, nil, ctx); err != nil {
		t.Fatalf("GroupItemPruneByChannelModels 返回错误: %v", err)
	}
	remaining, err := GroupItemList(group.ID, ctx)
	if err != nil {
		t.Fatalf("list group items: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("空模型列表应删除该渠道所有分组项, got %+v", remaining)
	}
}

func TestGroupListModelExcludesDisabledGroups(t *testing.T) {
	// 构造两个分组：一个启用，一个禁用；确认禁用的不出现在模型列表。
	g1 := model.Group{ID: 101, Name: "enabled-group", Enabled: true}
	g2 := model.Group{ID: 102, Name: "disabled-group", Enabled: false}
	groupCache.Set(g1.ID, g1)
	groupCache.Set(g2.ID, g2)
	defer func() {
		groupCache.Del(g1.ID)
		groupCache.Del(g2.ID)
	}()

	models, err := GroupListModel(context.Background())
	if err != nil {
		t.Fatalf("GroupListModel 返回错误: %v", err)
	}

	foundEnabled := false
	foundDisabled := false
	for _, m := range models {
		if m == g1.Name {
			foundEnabled = true
		}
		if m == g2.Name {
			foundDisabled = true
		}
	}
	if !foundEnabled {
		t.Errorf("GroupListModel 未返回启用分组 %q", g1.Name)
	}
	if foundDisabled {
		t.Errorf("GroupListModel 返回了禁用分组 %q，期望被过滤", g2.Name)
	}
}
