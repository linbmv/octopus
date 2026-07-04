package health

import (
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// HealthKey 健康状态的唯一标识
type HealthKey struct {
	ChannelID int
	KeyID     int    // 0 表示聚合到渠道级
	Model     string // "*" 表示聚合到渠道级
}

// HealthOutcome 事件结果类型
type HealthOutcome int

const (
	OutcomeSuccess HealthOutcome = iota
	OutcomeFirstTokenTimeout
	OutcomeNetworkError
	OutcomeClientCancel // 客户端主动取消
	OutcomeClientError  // 客户端配置错误
	OutcomeRateLimit
	OutcomeModelError
	OutcomeFormatError
	OutcomeUpstreamError
)

// String 返回 outcome 的字符串表示
func (o HealthOutcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeFirstTokenTimeout:
		return "first_token_timeout"
	case OutcomeNetworkError:
		return "network_error"
	case OutcomeClientCancel:
		return "client_cancel"
	case OutcomeClientError:
		return "client_error"
	case OutcomeRateLimit:
		return "rate_limit"
	case OutcomeModelError:
		return "model_error"
	case OutcomeFormatError:
		return "format_error"
	case OutcomeUpstreamError:
		return "upstream_error"
	default:
		return "unknown"
	}
}

// HealthEvent 健康事件
type HealthEvent struct {
	Level          errorclass.ErrorLevel
	Outcome        HealthOutcome
	HTTPStatus     int
	FirstTokenTime time.Duration
	TimeoutBudget  time.Duration
	At             time.Time
}

// HealthStats 健康统计
type HealthStats struct {
	TotalCount                 int64
	SuccessCount               int64
	TimeoutCount               int64
	NetworkCount               int64
	CancelCount                int64
	RateLimitCount             int64
	ModelErrorCount            int64
	FormatCount                int64
	KeyErrorCount              int64 // Key 级错误计数
	AutoFirstTokenTimeoutCount int64

	// 滑动窗口（最近 N 个事件）
	RecentResults []bool // true=success, false=failure

	// 成功率
	SuccessRate float64

	// 连续计数器
	ConsecutiveSuccess int
	ConsecutiveFailure int
	ConsecutiveTimeout int

	// 延迟分位数（毫秒）
	FirstTokenP50 time.Duration
	FirstTokenP95 time.Duration
	FirstTokenP99 time.Duration

	// 变异系数
	CV float64

	// 延迟估计器（不序列化，使用 EstimatorSnapshot）
	Estimator PercentileEstimator `json:"-"`

	// 最后事件时间
	LastEventAt time.Time
}

// HealthConfig 健康配置
type HealthConfig struct {
	// 超时参数
	MinTimeout                   time.Duration
	MaxTimeout                   time.Duration
	DefaultTimeout               time.Duration
	ColdStartTimeout             time.Duration
	MinSamplesForAdaptiveTimeout int
	MinAdaptiveTimeout           time.Duration
	SlowModelMinAdaptiveTimeout  time.Duration
	SlowModelMultiplier          float64
	TimeoutRateBackoffThreshold  float64
	TimeoutRateBackoffMultiplier float64

	// CV 阈值
	StableCV             float64
	ModerateCV           float64
	StableMultiplier     float64
	ModerateMultiplier   float64
	HighJitterMultiplier float64

	// 健康度参数
	WindowSize            int
	MinHealthScore        float64
	FastRecoveryThreshold int
	FastRecoveryScore     float64

	// 贝叶斯先验
	PriorSuccess           float64
	PriorTotal             float64
	MinSamplesForPosterior int

	// 失败样本权重
	TimeoutSampleWeight float64
	NetworkErrorWeight  float64

	// Estimator 配置
	EstimatorConfig EstimatorConfig
}

// DefaultHealthConfig 默认配置
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		MinTimeout:                   5 * time.Second,
		MaxTimeout:                   40 * time.Second,
		DefaultTimeout:               15 * time.Second,
		ColdStartTimeout:             20 * time.Second,
		MinSamplesForAdaptiveTimeout: 30,
		MinAdaptiveTimeout:           15 * time.Second,
		SlowModelMinAdaptiveTimeout:  25 * time.Second,
		SlowModelMultiplier:          1.30,
		TimeoutRateBackoffThreshold:  0.20,
		TimeoutRateBackoffMultiplier: 1.25,

		StableCV:             0.3,
		ModerateCV:           0.8,
		StableMultiplier:     1.10,
		ModerateMultiplier:   1.30,
		HighJitterMultiplier: 1.50,

		WindowSize:            50,
		MinHealthScore:        0.05,
		FastRecoveryThreshold: 5,
		FastRecoveryScore:     0.5,

		PriorSuccess:           7.0,
		PriorTotal:             10.0,
		MinSamplesForPosterior: 10,

		TimeoutSampleWeight: 1.0,
		NetworkErrorWeight:  0.5,

		EstimatorConfig: DefaultEstimatorConfig(),
	}
}

// ChannelHealth 渠道健康状态
type ChannelHealth struct {
	Key    HealthKey
	Stats  HealthStats
	Score  float64 // 健康度评分 [0, 1]
	Config HealthConfig

	mu sync.RWMutex
}

// NewChannelHealth 创建渠道健康状态
func NewChannelHealth(key HealthKey, config HealthConfig) *ChannelHealth {
	return &ChannelHealth{
		Key: key,
		Stats: HealthStats{
			RecentResults: make([]bool, 0, config.WindowSize),
			Estimator:     NewEstimator(config.EstimatorConfig),
		},
		Score:  1.0, // 初始满分
		Config: config,
	}
}

// OnEvent 处理健康事件
func (h *ChannelHealth) OnEvent(event HealthEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Stats.TotalCount++
	h.Stats.LastEventAt = event.At

	switch event.Outcome {
	case OutcomeSuccess:
		h.recordWindowLocked(true)
		h.Stats.SuccessCount++
		h.Stats.ConsecutiveSuccess++
		h.Stats.ConsecutiveFailure = 0
		h.Stats.ConsecutiveTimeout = 0

		// 记录成功样本的延迟
		if event.FirstTokenTime > 0 {
			h.Stats.Estimator.Add(float64(event.FirstTokenTime.Milliseconds()), 1.0)
		}

	case OutcomeFirstTokenTimeout:
		h.recordWindowLocked(false)
		h.Stats.TimeoutCount++
		h.Stats.AutoFirstTokenTimeoutCount++
		h.Stats.ConsecutiveSuccess = 0
		h.Stats.ConsecutiveFailure++
		h.Stats.ConsecutiveTimeout++

		// 记录超时样本（关键修复：使用权重 1.0）
		if event.TimeoutBudget > 0 {
			h.Stats.Estimator.Add(
				float64(event.TimeoutBudget.Milliseconds()),
				h.Config.TimeoutSampleWeight,
			)
		}

	case OutcomeNetworkError:
		h.recordWindowLocked(false)
		h.Stats.NetworkCount++
		h.Stats.ConsecutiveSuccess = 0
		h.Stats.ConsecutiveFailure++

	case OutcomeClientCancel:
		// 客户端取消不计入成功率
		h.Stats.CancelCount++
		return

	case OutcomeClientError:
		// 客户端配置错误不计入成功率
		h.Stats.CancelCount++
		return

	case OutcomeRateLimit:
		// 限流不计入成功率（交给 Key 冷却处理）
		h.Stats.RateLimitCount++
		return

	case OutcomeModelError:
		h.recordWindowLocked(false)
		h.Stats.ModelErrorCount++
		h.Stats.ConsecutiveSuccess = 0
		h.Stats.ConsecutiveFailure++

	case OutcomeFormatError:
		h.recordWindowLocked(false)
		h.Stats.FormatCount++
		h.Stats.ConsecutiveSuccess = 0
		h.Stats.ConsecutiveFailure++

	case OutcomeUpstreamError:
		h.recordWindowLocked(false)
		h.Stats.ConsecutiveSuccess = 0
		h.Stats.ConsecutiveFailure++
	}

	// 重新计算健康度和延迟分位数
	h.recomputeLocked()
}

// recordWindowLocked 记录事件到滑动窗口
func (h *ChannelHealth) recordWindowLocked(success bool) {
	h.Stats.RecentResults = append(h.Stats.RecentResults, success)

	// 超过窗口大小，移除最旧的
	if len(h.Stats.RecentResults) > h.Config.WindowSize {
		h.Stats.RecentResults = h.Stats.RecentResults[1:]
	}
}

// recomputeLocked 重新计算健康度和统计指标
func (h *ChannelHealth) recomputeLocked() {
	// 计算成功率
	if len(h.Stats.RecentResults) > 0 {
		successCount := 0
		for _, success := range h.Stats.RecentResults {
			if success {
				successCount++
			}
		}
		h.Stats.SuccessRate = float64(successCount) / float64(len(h.Stats.RecentResults))
	} else {
		// 使用贝叶斯先验
		h.Stats.SuccessRate = h.Config.PriorSuccess / h.Config.PriorTotal
	}

	// 计算延迟分位数
	if h.Stats.Estimator.Count() > 0 {
		if p50, ok := h.Stats.Estimator.Quantile(0.50); ok {
			h.Stats.FirstTokenP50 = time.Duration(p50) * time.Millisecond
		}
		if p95, ok := h.Stats.Estimator.Quantile(0.95); ok {
			h.Stats.FirstTokenP95 = time.Duration(p95) * time.Millisecond
		}
		if p99, ok := h.Stats.Estimator.Quantile(0.99); ok {
			h.Stats.FirstTokenP99 = time.Duration(p99) * time.Millisecond
		}

		// 计算变异系数 CV = (P95-P50) / P50
		if h.Stats.FirstTokenP50 > 0 {
			h.Stats.CV = float64(h.Stats.FirstTokenP95-h.Stats.FirstTokenP50) / float64(h.Stats.FirstTokenP50)
		}
	}

	// 计算健康度评分
	h.Score = h.Stats.SuccessRate

	// 快速恢复机制
	if h.Stats.ConsecutiveSuccess >= h.Config.FastRecoveryThreshold {
		if h.Score < h.Config.FastRecoveryScore {
			h.Score = h.Config.FastRecoveryScore
		}
	}

	// 保底分数
	if h.Score < h.Config.MinHealthScore {
		h.Score = h.Config.MinHealthScore
	}

	// 上限
	if h.Score > 1.0 {
		h.Score = 1.0
	}
}

// GetTimeout 获取自适应超时
func (h *ChannelHealth) GetTimeout() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// 样本数不足，使用冷启动超时
	if h.Stats.TotalCount < int64(h.Config.MinSamplesForAdaptiveTimeout) {
		return h.Config.ColdStartTimeout
	}

	// 获取 P50 和 P95
	p50, ok50 := h.Stats.Estimator.Quantile(0.50)
	p95, ok95 := h.Stats.Estimator.Quantile(0.95)

	if !ok50 || !ok95 || p50 <= 0 || p95 <= 0 || p95 < p50 {
		return h.Config.DefaultTimeout
	}

	// 根据 CV 选择 multiplier
	cv := h.Stats.CV
	multiplier := h.Config.HighJitterMultiplier

	if cv < h.Config.StableCV {
		multiplier = h.Config.StableMultiplier
	} else if cv < h.Config.ModerateCV {
		multiplier = h.Config.ModerateMultiplier
	}

	if isSlowFirstTokenModel(h.Key.Model) && h.Config.SlowModelMultiplier > 0 {
		multiplier *= h.Config.SlowModelMultiplier
	}

	if timeoutRate := h.timeoutRateLocked(); timeoutRate >= h.Config.TimeoutRateBackoffThreshold && h.Config.TimeoutRateBackoffMultiplier > 0 {
		multiplier *= h.Config.TimeoutRateBackoffMultiplier
	}

	// 计算超时 = P95 × multiplier
	timeout := time.Duration(p95*multiplier) * time.Millisecond

	// 边界限制
	if timeout < h.Config.MinTimeout {
		timeout = h.Config.MinTimeout
	}
	if timeout < h.Config.MinAdaptiveTimeout {
		timeout = h.Config.MinAdaptiveTimeout
	}
	if isSlowFirstTokenModel(h.Key.Model) && timeout < h.Config.SlowModelMinAdaptiveTimeout {
		timeout = h.Config.SlowModelMinAdaptiveTimeout
	}
	if timeout > h.Config.MaxTimeout {
		timeout = h.Config.MaxTimeout
	}

	// 连续超时保护：增加 15%
	if h.Stats.ConsecutiveTimeout >= 2 {
		timeout = time.Duration(float64(timeout) * 1.15)
		if timeout > h.Config.MaxTimeout {
			timeout = h.Config.MaxTimeout
		}
	}

	return timeout
}

func (h *ChannelHealth) timeoutRateLocked() float64 {
	if h.Stats.TotalCount <= 0 {
		return 0
	}
	return float64(h.Stats.TimeoutCount) / float64(h.Stats.TotalCount)
}

func isSlowFirstTokenModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "thinking") ||
		strings.Contains(model, "opus") ||
		strings.Contains(model, "reasoning") ||
		strings.Contains(model, "long-context") ||
		strings.Contains(model, "long_context") ||
		strings.Contains(model, "200k") ||
		strings.Contains(model, "1m")
}

// GetScore 获取健康度评分
func (h *ChannelHealth) GetScore() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Score
}

// GetStats 获取统计信息（只读副本）
func (h *ChannelHealth) GetStats() HealthStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// 返回副本，避免外部修改
	stats := h.Stats
	stats.RecentResults = make([]bool, len(h.Stats.RecentResults))
	copy(stats.RecentResults, h.Stats.RecentResults)

	return stats
}

// RestoreStats replaces persisted counters and estimator-derived values, then
// recomputes derived score fields while holding the health lock.
func (h *ChannelHealth) RestoreStats(stats HealthStats, score float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	estimator := h.Stats.Estimator
	h.Stats = stats
	h.Stats.Estimator = estimator
	if h.Stats.RecentResults == nil {
		h.Stats.RecentResults = make([]bool, 0, h.Config.WindowSize)
	}
	h.Score = score
	h.recomputeLocked()
}
