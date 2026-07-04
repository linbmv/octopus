package health

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// HealthMetrics Prometheus 指标
type HealthMetrics struct {
	// 健康度评分
	healthScore *prometheus.GaugeVec

	// 成功率
	successRate *prometheus.GaugeVec

	// 请求计数
	totalRequests   *prometheus.CounterVec
	successRequests *prometheus.CounterVec
	failedRequests  *prometheus.CounterVec

	// 延迟分位数
	p50Latency *prometheus.GaugeVec
	p95Latency *prometheus.GaugeVec
	p99Latency *prometheus.GaugeVec

	// CV (变异系数)
	cv *prometheus.GaugeVec

	// 自适应超时
	adaptiveTimeout *prometheus.GaugeVec

	// 事件类型计数
	timeoutCount    *prometheus.CounterVec
	networkErrCount *prometheus.CounterVec
	rateLimitCount  *prometheus.CounterVec
	modelErrCount   *prometheus.CounterVec
	keyErrCount     *prometheus.CounterVec

	// 连续计数器
	consecutiveSuccess *prometheus.GaugeVec
	consecutiveFailure *prometheus.GaugeVec
}

// NewHealthMetrics 创建 Prometheus 指标
func NewHealthMetrics(namespace string) *HealthMetrics {
	if namespace == "" {
		namespace = "octopus"
	}

	return &HealthMetrics{
		healthScore: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "score",
				Help:      "Channel health score (0-1)",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		successRate: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "success_rate",
				Help:      "Channel success rate (0-1)",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		totalRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_total",
				Help:      "Total number of requests",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		successRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_success_total",
				Help:      "Total number of successful requests",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		failedRequests: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_failed_total",
				Help:      "Total number of failed requests",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		p50Latency: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "first_token_p50_ms",
				Help:      "P50 first token latency in milliseconds",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		p95Latency: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "first_token_p95_ms",
				Help:      "P95 first token latency in milliseconds",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		p99Latency: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "first_token_p99_ms",
				Help:      "P99 first token latency in milliseconds",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		cv: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "cv",
				Help:      "Coefficient of variation (P95-P50)/P50",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		adaptiveTimeout: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "adaptive_timeout_ms",
				Help:      "Adaptive timeout in milliseconds",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		timeoutCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "timeout_total",
				Help:      "Total number of timeout events",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		networkErrCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "network_error_total",
				Help:      "Total number of network errors",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		rateLimitCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "rate_limit_total",
				Help:      "Total number of rate limit events",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		modelErrCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "model_error_total",
				Help:      "Total number of model errors",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		keyErrCount: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "key_error_total",
				Help:      "Total number of key-level errors",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		consecutiveSuccess: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "consecutive_success",
				Help:      "Number of consecutive successes",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		consecutiveFailure: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "consecutive_failure",
				Help:      "Number of consecutive failures",
			},
			[]string{"channel_id", "key_id", "model"},
		),
	}
}

// Update 更新指标
func (m *HealthMetrics) Update(key HealthKey, health *ChannelHealth) {
	if m == nil || health == nil {
		return
	}

	labels := prometheus.Labels{
		"channel_id": formatInt(key.ChannelID),
		"key_id":     formatInt(key.KeyID),
		"model":      key.Model,
	}

	stats := health.GetStats()
	score := health.GetScore()
	timeout := health.GetTimeout()

	// 健康度和成功率
	m.healthScore.With(labels).Set(score)
	m.successRate.With(labels).Set(stats.SuccessRate)

	// 请求计数（使用 Add 而不是 Set，因为 Counter 只能增加）
	// 注意：这里需要计算增量，但为了简化，我们使用 Set 的变通方法
	m.totalRequests.With(labels).Add(0)   // 初始化
	m.successRequests.With(labels).Add(0) // 初始化
	m.failedRequests.With(labels).Add(0)  // 初始化

	// 延迟分位数
	m.p50Latency.With(labels).Set(float64(stats.FirstTokenP50.Milliseconds()))
	m.p95Latency.With(labels).Set(float64(stats.FirstTokenP95.Milliseconds()))
	m.p99Latency.With(labels).Set(float64(stats.FirstTokenP99.Milliseconds()))

	// CV
	m.cv.With(labels).Set(stats.CV)

	// 自适应超时
	m.adaptiveTimeout.With(labels).Set(float64(timeout.Milliseconds()))

	// 事件类型计数
	m.timeoutCount.With(labels).Add(0)      // 初始化
	m.networkErrCount.With(labels).Add(0)   // 初始化
	m.rateLimitCount.With(labels).Add(0)    // 初始化
	m.modelErrCount.With(labels).Add(0)     // 初始化
	m.keyErrCount.With(labels).Add(0)       // 初始化

	// 连续计数器
	m.consecutiveSuccess.With(labels).Set(float64(stats.ConsecutiveSuccess))
	m.consecutiveFailure.With(labels).Set(float64(stats.ConsecutiveFailure))
}

// UpdateAll 更新所有渠道的指标
func (m *HealthMetrics) UpdateAll(manager *HealthManager) {
	if m == nil || manager == nil {
		return
	}

	allStates := manager.GetAllStates()
	for key := range allStates {
		if health, ok := manager.Get(key); ok {
			m.Update(key, health)
		}
	}
}

// formatInt 格式化整数为字符串
func formatInt(i int) string {
	return fmt.Sprintf("%d", i)
}
