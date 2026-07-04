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

	// 请求计数快照
	totalRequests   *prometheus.GaugeVec
	successRequests *prometheus.GaugeVec
	failedRequests  *prometheus.GaugeVec

	// 延迟分位数
	p50Latency *prometheus.GaugeVec
	p95Latency *prometheus.GaugeVec
	p99Latency *prometheus.GaugeVec

	// CV (变异系数)
	cv *prometheus.GaugeVec

	// 自适应超时
	adaptiveTimeout *prometheus.GaugeVec

	// 事件类型计数快照
	timeoutCount               *prometheus.GaugeVec
	autoFirstTokenTimeoutCount *prometheus.GaugeVec
	networkErrCount            *prometheus.GaugeVec
	rateLimitCount             *prometheus.GaugeVec
	modelErrCount              *prometheus.GaugeVec
	keyErrCount                *prometheus.GaugeVec

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

		totalRequests: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_count",
				Help:      "Current health snapshot total request count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		successRequests: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_success_count",
				Help:      "Current health snapshot successful request count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		failedRequests: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "requests_failed_count",
				Help:      "Current health snapshot failed request count",
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

		timeoutCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "timeout_count",
				Help:      "Current health snapshot timeout event count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		autoFirstTokenTimeoutCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "auto_first_token_timeout_count",
				Help:      "Current health snapshot automatic adaptive first-token timeout count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		networkErrCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "network_error_count",
				Help:      "Current health snapshot network error count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		rateLimitCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "rate_limit_count",
				Help:      "Current health snapshot rate limit event count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		modelErrCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "model_error_count",
				Help:      "Current health snapshot model error count",
			},
			[]string{"channel_id", "key_id", "model"},
		),

		keyErrCount: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "health",
				Name:      "key_error_count",
				Help:      "Current health snapshot key-level error count",
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

	// 请求计数快照
	m.totalRequests.With(labels).Set(float64(stats.TotalCount))
	m.successRequests.With(labels).Set(float64(stats.SuccessCount))
	m.failedRequests.With(labels).Set(float64(stats.TotalCount - stats.SuccessCount))

	// 延迟分位数
	m.p50Latency.With(labels).Set(float64(stats.FirstTokenP50.Milliseconds()))
	m.p95Latency.With(labels).Set(float64(stats.FirstTokenP95.Milliseconds()))
	m.p99Latency.With(labels).Set(float64(stats.FirstTokenP99.Milliseconds()))

	// CV
	m.cv.With(labels).Set(stats.CV)

	// 自适应超时
	m.adaptiveTimeout.With(labels).Set(float64(timeout.Milliseconds()))

	// 事件类型计数快照
	m.timeoutCount.With(labels).Set(float64(stats.TimeoutCount))
	m.autoFirstTokenTimeoutCount.With(labels).Set(float64(stats.AutoFirstTokenTimeoutCount))
	m.networkErrCount.With(labels).Set(float64(stats.NetworkCount))
	m.rateLimitCount.With(labels).Set(float64(stats.RateLimitCount))
	m.modelErrCount.With(labels).Set(float64(stats.ModelErrorCount))
	m.keyErrCount.With(labels).Set(float64(stats.KeyErrorCount))

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
