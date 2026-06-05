package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

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
