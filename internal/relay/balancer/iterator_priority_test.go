package balancer

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

// TestApplyCandidateRanks_PriorityFirst 验证方案A：Priority 为第一排序键
func TestApplyCandidateRanks_PriorityFirst(t *testing.T) {
	tests := []struct {
		name       string
		candidates []model.GroupItem
		ranks      map[int]int
		wantOrder  []int // 期望的 ID 顺序
	}{
		{
			name: "Priority 不同时，按 Priority 排序（忽略 capability rank）",
			candidates: []model.GroupItem{
				{ID: 1, Priority: 10, ChannelID: 100}, // 低优先级，但 capability rank 好
				{ID: 2, Priority: 1, ChannelID: 200},  // 高优先级，但 capability rank 差
			},
			ranks:     map[int]int{1: 1, 2: 99}, // rank 反向
			wantOrder: []int{2, 1},               // 期望按 Priority 排序：2 (priority=1) → 1 (priority=10)
		},
		{
			name: "Priority 相同时，按 capability rank 排序",
			candidates: []model.GroupItem{
				{ID: 1, Priority: 5, ChannelID: 100},
				{ID: 2, Priority: 5, ChannelID: 200},
				{ID: 3, Priority: 5, ChannelID: 300},
			},
			ranks:     map[int]int{1: 10, 2: 1, 3: 5},
			wantOrder: []int{2, 3, 1}, // capability rank 升序
		},
		{
			name: "混合场景：3个 Priority 分组，每组内按 capability rank 排序",
			candidates: []model.GroupItem{
				{ID: 1, Priority: 1, ChannelID: 100},
				{ID: 2, Priority: 1, ChannelID: 200},
				{ID: 3, Priority: 5, ChannelID: 300},
				{ID: 4, Priority: 5, ChannelID: 400},
				{ID: 5, Priority: 10, ChannelID: 500},
			},
			ranks: map[int]int{1: 20, 2: 10, 3: 15, 4: 5, 5: 1},
			wantOrder: []int{
				2, 1, // Priority=1 组：rank 10 < 20
				4, 3, // Priority=5 组：rank 5 < 15
				5,    // Priority=10 组：只有一个
			},
		},
		{
			name: "无 rank 数据时，仅按 Priority 排序",
			candidates: []model.GroupItem{
				{ID: 3, Priority: 10, ChannelID: 300},
				{ID: 1, Priority: 1, ChannelID: 100},
				{ID: 2, Priority: 5, ChannelID: 200},
			},
			ranks:     nil,
			wantOrder: []int{3, 1, 2}, // 输入顺序不变（无 rank 时 applyCandidateRanks 直接返回）
		},
		{
			name: "部分 item 无 rank：无 rank 的排到最后",
			candidates: []model.GroupItem{
				{ID: 1, Priority: 5, ChannelID: 100},
				{ID: 2, Priority: 5, ChannelID: 200},
				{ID: 3, Priority: 5, ChannelID: 300},
			},
			ranks:     map[int]int{1: 10, 3: 5}, // ID=2 无 rank
			wantOrder: []int{3, 1, 2},           // rank 5 < 10 < (1<<30)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := make([]model.GroupItem, len(tt.candidates))
			copy(candidates, tt.candidates)

			applyCandidateRanks(candidates, tt.ranks)

			got := make([]int, len(candidates))
			for i, item := range candidates {
				got[i] = item.ID
			}

			if len(got) != len(tt.wantOrder) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.wantOrder))
			}
			for i := range got {
				if got[i] != tt.wantOrder[i] {
					t.Errorf("order mismatch at index %d: got %v, want %v", i, got, tt.wantOrder)
					break
				}
			}
		})
	}
}

// TestApplyCandidateRanks_RealWorldScenario 真实场景：用户配置成本优先
func TestApplyCandidateRanks_RealWorldScenario(t *testing.T) {
	// 用户配置：
	// 1. 自建API (priority=1, 成本最低)
	// 2. Anthropic官方 (priority=5, 备用)
	// 3. OpenAI官方 (priority=10, 最后选择)
	//
	// Capability rank 假设：
	// - OpenAI 官方最可靠 (rank=1)
	// - Anthropic 次之 (rank=2)
	// - 自建API 证据不足 (rank=99)
	candidates := []model.GroupItem{
		{ID: 3, Priority: 10, ChannelID: 300},
		{ID: 1, Priority: 1, ChannelID: 100},
		{ID: 2, Priority: 5, ChannelID: 200},
	}
	ranks := map[int]int{1: 99, 2: 2, 3: 1}

	applyCandidateRanks(candidates, ranks)

	// 期望顺序：自建API → Anthropic → OpenAI（严格按 Priority）
	wantOrder := []int{1, 2, 3}
	got := make([]int, len(candidates))
	for i, item := range candidates {
		got[i] = item.ID
	}

	for i := range got {
		if got[i] != wantOrder[i] {
			t.Errorf("用户成本策略被覆盖：got %v, want %v", got, wantOrder)
			t.Logf("排序后渠道: %+v", candidates)
			break
		}
	}
}
