package health

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// HistogramEstimator 直方图分位数估计器（降级备选）
type HistogramEstimator struct {
	mu      sync.RWMutex
	buckets []float64 // 分桶边界（已排序）
	counts  []int64   // 每个桶的计数
	count   int64     // 总样本数
	config  EstimatorConfig
}

// NewHistogramEstimator 创建 Histogram 估计器
func NewHistogramEstimator(config EstimatorConfig) *HistogramEstimator {
	buckets := config.HistogramBuckets
	if len(buckets) == 0 {
		// 默认分桶
		buckets = []float64{1000, 2000, 5000, 10000, 15000, 20000, 30000, 40000}
	}

	// 确保分桶已排序
	sortedBuckets := make([]float64, len(buckets))
	copy(sortedBuckets, buckets)
	sort.Float64s(sortedBuckets)

	return &HistogramEstimator{
		buckets: sortedBuckets,
		counts:  make([]int64, len(sortedBuckets)+1), // +1 for overflow bucket
		count:   0,
		config:  config,
	}
}

// Add 添加样本
func (e *HistogramEstimator) Add(value float64, weight float64) error {
	if value < 0 {
		return errors.New("value must be non-negative")
	}
	if weight <= 0 {
		return errors.New("weight must be positive")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// 找到对应的桶
	bucketIndex := e.findBucket(value)

	// 权重转换为整数计数（简化实现）
	weightCount := int64(weight + 0.5)
	if weightCount < 1 {
		weightCount = 1
	}

	e.counts[bucketIndex] += weightCount
	e.count += weightCount

	return nil
}

// findBucket 找到 value 对应的桶索引
func (e *HistogramEstimator) findBucket(value float64) int {
	// 二分查找
	idx := sort.Search(len(e.buckets), func(i int) bool {
		return e.buckets[i] > value
	})
	return idx
}

// Quantile 获取分位数（线性插值）
func (e *HistogramEstimator) Quantile(q float64) (float64, bool) {
	if q < 0 || q > 1 {
		return 0, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.count == 0 {
		return 0, false
	}

	// 计算目标样本数
	targetCount := float64(e.count) * q

	// 累积计数找到目标桶
	cumCount := int64(0)
	for i, count := range e.counts {
		cumCount += count
		if float64(cumCount) >= targetCount {
			// 找到目标桶，返回桶的上界
			if i < len(e.buckets) {
				return e.buckets[i], true
			}
			// overflow bucket，返回最后一个桶边界
			return e.buckets[len(e.buckets)-1] * 1.5, true
		}
	}

	// 不应该到这里
	return 0, false
}

// Count 返回样本总数
func (e *HistogramEstimator) Count() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.count
}

// Snapshot 创建快照
func (e *HistogramEstimator) Snapshot() EstimatorSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 序列化为 JSON
	data, err := json.Marshal(map[string]interface{}{
		"buckets": e.buckets,
		"counts":  e.counts,
	})
	if err != nil {
		return EstimatorSnapshot{
			Type:      "histogram",
			Version:   "v1",
			Count:     e.count,
			UpdatedAt: time.Now(),
		}
	}

	return EstimatorSnapshot{
		Type:      "histogram",
		Version:   "v1",
		Data:      data,
		Count:     e.count,
		UpdatedAt: time.Now(),
	}
}

// Restore 从快照恢复
func (e *HistogramEstimator) Restore(snapshot EstimatorSnapshot) error {
	if snapshot.Type != "histogram" {
		return errors.New("snapshot type mismatch")
	}

	if len(snapshot.Data) == 0 {
		return errors.New("empty snapshot data")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// 反序列化
	var data struct {
		Buckets []float64 `json:"buckets"`
		Counts  []int64   `json:"counts"`
	}

	if err := json.Unmarshal(snapshot.Data, &data); err != nil {
		return err
	}

	e.buckets = data.Buckets
	e.counts = data.Counts
	e.count = snapshot.Count

	return nil
}

// Reset 清空所有数据
func (e *HistogramEstimator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.counts = make([]int64, len(e.buckets)+1)
	e.count = 0
}

// MarshalJSON 实现 JSON 序列化（用于调试）
func (e *HistogramEstimator) MarshalJSON() ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	p50, _ := e.Quantile(0.50)
	p95, _ := e.Quantile(0.95)
	p99, _ := e.Quantile(0.99)

	return json.Marshal(map[string]interface{}{
		"type":  "histogram",
		"count": e.count,
		"p50":   p50,
		"p95":   p95,
		"p99":   p99,
	})
}
