package health

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/influxdata/tdigest"
)

// TDigestEstimator T-Digest 分位数估计器
type TDigestEstimator struct {
	mu      sync.RWMutex
	digest  *tdigest.TDigest
	count   int64
	config  EstimatorConfig
}

// NewTDigestEstimator 创建 T-Digest 估计器
func NewTDigestEstimator(config EstimatorConfig) (*TDigestEstimator, error) {
	compression := config.TDigestCompression
	if compression <= 0 {
		compression = 100
	}

	digest := tdigest.NewWithCompression(float64(compression))
	if digest == nil {
		return nil, errors.New("failed to create T-Digest")
	}

	return &TDigestEstimator{
		digest: digest,
		count:  0,
		config: config,
	}, nil
}

// Add 添加样本
func (e *TDigestEstimator) Add(value float64, weight float64) error {
	if value < 0 {
		return errors.New("value must be non-negative")
	}
	if weight <= 0 {
		return errors.New("weight must be positive")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.digest.Add(value, weight)
	e.count++

	return nil
}

// Quantile 获取分位数
func (e *TDigestEstimator) Quantile(q float64) (float64, bool) {
	if q < 0 || q > 1 {
		return 0, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.count == 0 {
		return 0, false
	}

	result := e.digest.Quantile(q)
	if result < 0 {
		return 0, false
	}

	return result, true
}

// Count 返回样本总数
func (e *TDigestEstimator) Count() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.count
}

// Snapshot 创建快照
func (e *TDigestEstimator) Snapshot() EstimatorSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// tdigest 库不支持 MarshalBinary，使用 JSON 序列化 centroids
	centroids := e.digest.Centroids()
	data, err := json.Marshal(centroids)
	if err != nil {
		// 序列化失败，返回空快照
		return EstimatorSnapshot{
			Type:      "tdigest",
			Version:   "v1",
			Count:     e.count,
			UpdatedAt: time.Now(),
		}
	}

	return EstimatorSnapshot{
		Type:      "tdigest",
		Version:   "v1",
		Data:      data,
		Count:     e.count,
		UpdatedAt: time.Now(),
	}
}

// Restore 从快照恢复
func (e *TDigestEstimator) Restore(snapshot EstimatorSnapshot) error {
	if snapshot.Type != "tdigest" {
		return errors.New("snapshot type mismatch")
	}

	if len(snapshot.Data) == 0 {
		return errors.New("empty snapshot data")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// 反序列化 centroids
	var centroids []tdigest.Centroid
	if err := json.Unmarshal(snapshot.Data, &centroids); err != nil {
		return err
	}

	// 创建新的 T-Digest 并添加 centroids
	newDigest := tdigest.NewWithCompression(float64(e.config.TDigestCompression))
	centroidList := tdigest.NewCentroidList(centroids)
	newDigest.AddCentroidList(centroidList)

	e.digest = newDigest
	e.count = snapshot.Count

	return nil
}

// Reset 清空所有数据
func (e *TDigestEstimator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.digest = tdigest.NewWithCompression(float64(e.config.TDigestCompression))
	e.count = 0
}

// MarshalJSON 实现 JSON 序列化（用于调试）
func (e *TDigestEstimator) MarshalJSON() ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return json.Marshal(map[string]interface{}{
		"type":  "tdigest",
		"count": e.count,
		"p50":   e.digest.Quantile(0.50),
		"p95":   e.digest.Quantile(0.95),
		"p99":   e.digest.Quantile(0.99),
	})
}
