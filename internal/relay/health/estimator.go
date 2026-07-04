package health

import (
	"time"
)

// PercentileEstimator 分位数估计器接口
// 设计原则：抽象接口，不依赖具体实现（T-Digest/Histogram）
type PercentileEstimator interface {
	// Add 添加样本（带权重）
	Add(value float64, weight float64) error

	// Quantile 获取分位数（q in [0, 1]）
	// 返回值：(分位数值, 是否有效)
	Quantile(q float64) (float64, bool)

	// Count 返回样本总数
	Count() int64

	// Snapshot 创建快照（用于持久化）
	Snapshot() EstimatorSnapshot

	// Restore 从快照恢复
	Restore(snapshot EstimatorSnapshot) error

	// Reset 清空所有数据
	Reset()
}

// EstimatorSnapshot 估计器快照（用于持久化）
type EstimatorSnapshot struct {
	Type    string    `json:"type"`    // "tdigest" | "histogram"
	Version string    `json:"version"` // schema 版本
	Data    []byte    `json:"data"`    // 实现特定的序列化数据
	Count   int64     `json:"count"`   // 样本总数
	UpdatedAt time.Time `json:"updated_at"`
}

// EstimatorConfig 估计器配置
type EstimatorConfig struct {
	// T-Digest 配置
	TDigestCompression int // 压缩率（默认 100）
	TDigestMaxMergeSets int // 最大合并集合数（默认 5）

	// Histogram 配置（降级备选）
	HistogramBuckets []float64 // 分桶边界
}

// DefaultEstimatorConfig 默认配置
func DefaultEstimatorConfig() EstimatorConfig {
	return EstimatorConfig{
		TDigestCompression: 100,
		TDigestMaxMergeSets: 5,
		// 默认 histogram 分桶：1s, 2s, 5s, 10s, 15s, 20s, 30s, 40s
		HistogramBuckets: []float64{
			1000, 2000, 5000, 10000, 15000, 20000, 30000, 40000,
		},
	}
}

// NewEstimator 创建估计器
// 优先使用 T-Digest，失败时降级到 Histogram
func NewEstimator(config EstimatorConfig) PercentileEstimator {
	// 尝试创建 T-Digest
	if tdigest, err := NewTDigestEstimator(config); err == nil {
		return tdigest
	}

	// 降级到 Histogram
	return NewHistogramEstimator(config)
}
