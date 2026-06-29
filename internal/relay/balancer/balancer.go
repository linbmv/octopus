package balancer

import (
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bestruirui/octopus/internal/model"
)

var roundRobinCounter uint64

// Balancer 根据负载均衡模式选择通道
type Balancer interface {
	// Candidates 返回按策略排序的候选列表
	// 调用方在遍历候选列表时自行检查熔断状态
	Candidates(items []model.GroupItem) []model.GroupItem
}

// GetBalancer 根据模式返回对应的负载均衡器
func GetBalancer(mode model.GroupMode) Balancer {
	switch mode {
	case model.GroupModeRoundRobin:
		return &RoundRobin{}
	case model.GroupModeRandom:
		return &Random{}
	case model.GroupModeFailover:
		return &Failover{}
	case model.GroupModeWeighted:
		return &Weighted{}
	default:
		return &RoundRobin{}
	}
}

// RoundRobin 轮询：从上次位置开始轮转排列
type RoundRobin struct{}

func (b *RoundRobin) Candidates(items []model.GroupItem) []model.GroupItem {
	n := len(items)
	if n == 0 {
		return nil
	}
	idx := int(atomic.AddUint64(&roundRobinCounter, 1) % uint64(n))
	result := make([]model.GroupItem, n)
	for i := 0; i < n; i++ {
		result[i] = items[(idx+i)%n]
	}
	return result
}

// Random 随机：随机打乱所有 items
type Random struct{}

func (b *Random) Candidates(items []model.GroupItem) []model.GroupItem {
	n := len(items)
	if n == 0 {
		return nil
	}
	result := make([]model.GroupItem, n)
	copy(result, items)
	rand.Shuffle(n, func(i, j int) {
		result[i], result[j] = result[j], result[i]
	})
	return result
}

// Failover 故障转移：按优先级排序
type Failover struct{}

func (b *Failover) Candidates(items []model.GroupItem) []model.GroupItem {
	if len(items) == 0 {
		return nil
	}
	return sortByPriority(items)
}

// Weighted 加权分配：按平滑加权轮询稳定分配流量。
type Weighted struct{}

var smoothWeightedState = &weightedRRState{groups: make(map[string]map[string]int)}

type weightedRRState struct {
	mu     sync.Mutex
	groups map[string]map[string]int
}

func (b *Weighted) Candidates(items []model.GroupItem) []model.GroupItem {
	n := len(items)
	if n == 0 {
		return nil
	}
	if n == 1 {
		result := make([]model.GroupItem, 1)
		copy(result, items)
		return result
	}

	totalWeight := 0
	weights := make([]int, n)
	for i, item := range items {
		w := item.Weight
		if w <= 0 {
			w = 1
		}
		weights[i] = w
		totalWeight += w
	}
	if totalWeight <= 0 {
		return sortByPriority(items)
	}

	groupKey := weightedGroupKey(items)
	smoothWeightedState.mu.Lock()
	defer smoothWeightedState.mu.Unlock()

	currentWeights := smoothWeightedState.groups[groupKey]
	if currentWeights == nil {
		currentWeights = make(map[string]int, n)
		smoothWeightedState.groups[groupKey] = currentWeights
	}

	selectedIdx := 0
	selectedWeight := 0
	for i, item := range items {
		itemKey := weightedItemKey(item)
		currentWeights[itemKey] += weights[i]
		current := currentWeights[itemKey]
		if i == 0 || current > selectedWeight || (current == selectedWeight && item.ID < items[selectedIdx].ID) {
			selectedIdx = i
			selectedWeight = current
		}
	}
	currentWeights[weightedItemKey(items[selectedIdx])] -= totalWeight

	result := make([]model.GroupItem, n)
	result[0] = items[selectedIdx]
	next := 1
	for i, item := range items {
		if i == selectedIdx {
			continue
		}
		result[next] = item
		next++
	}
	return result
}

func weightedGroupKey(items []model.GroupItem) string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, weightedItemKey(item))
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(key)
	}
	return b.String()
}

func weightedItemKey(item model.GroupItem) string {
	return strings.Join([]string{
		intString(item.ID),
		intString(item.ChannelID),
		intString(item.TargetGroupID),
		item.ModelName,
		item.Type,
	}, ":")
}

func intString(value int) string {
	return strconv.Itoa(value)
}

func sortByPriority(items []model.GroupItem) []model.GroupItem {
	sorted := make([]model.GroupItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})
	return sorted
}
