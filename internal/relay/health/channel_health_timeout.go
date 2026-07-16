package health

import (
	"strings"
	"time"
)

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

	multiplier := h.calculateMultiplier()
	timeout := h.calculateTimeout(p95, multiplier)
	return h.applyConsecutiveTimeoutProtection(timeout)
}

func (h *ChannelHealth) calculateMultiplier() float64 {
	cv := h.Stats.CV
	multiplier := h.Config.HighJitterMultiplier

	if cv < h.Config.StableCV {
		multiplier = h.Config.StableMultiplier
	} else if cv < h.Config.ModerateCV {
		multiplier = h.Config.ModerateMultiplier
	}

	slowModel := isSlowFirstTokenModelWithKeywords(h.Key.Model, h.Config.SlowModelKeywords)
	if slowModel && h.Config.SlowModelMultiplier > 0 {
		multiplier *= h.Config.SlowModelMultiplier
	}

	timeoutRate := h.timeoutRateLocked()
	if timeoutRate >= h.Config.TimeoutRateBackoffThreshold && h.Config.TimeoutRateBackoffMultiplier > 0 {
		multiplier *= h.Config.TimeoutRateBackoffMultiplier
	}

	// 防止 multiplier 过度叠加
	if h.Config.MaxMultiplierStack > 0 && multiplier > h.Config.MaxMultiplierStack {
		multiplier = h.Config.MaxMultiplierStack
	}

	return multiplier
}

func (h *ChannelHealth) calculateTimeout(p95, multiplier float64) time.Duration {
	timeout := time.Duration(p95*multiplier) * time.Millisecond

	// 边界限制
	if timeout < h.Config.MinTimeout {
		timeout = h.Config.MinTimeout
	}
	if timeout < h.Config.MinAdaptiveTimeout {
		timeout = h.Config.MinAdaptiveTimeout
	}

	slowModel := isSlowFirstTokenModelWithKeywords(h.Key.Model, h.Config.SlowModelKeywords)
	if slowModel && timeout < h.Config.SlowModelMinAdaptiveTimeout {
		timeout = h.Config.SlowModelMinAdaptiveTimeout
	}
	if timeout > h.Config.MaxTimeout {
		timeout = h.Config.MaxTimeout
	}

	return timeout
}

func (h *ChannelHealth) applyConsecutiveTimeoutProtection(timeout time.Duration) time.Duration {
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

// RecordShadowTimeout 记录 shadow 模式下的超时事件
func (h *ChannelHealth) RecordShadowTimeout() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Stats.ShadowAutoTimeoutWouldTrigger++
	// 使用滑动窗口大小作为分母
	windowSize := h.Config.WindowSize
	if windowSize <= 0 {
		windowSize = 50
	}
	// 只保留最近 N 次的统计
	if h.Stats.TotalCount > 0 && h.Stats.TotalCount <= int64(windowSize) {
		h.Stats.ShadowLastWindowSize = int(h.Stats.TotalCount)
	} else {
		h.Stats.ShadowLastWindowSize = windowSize
	}
}

func isSlowFirstTokenModelWithKeywords(model string, keywords []string) bool {
	model = strings.ToLower(model)
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(strings.ToLower(keyword))
		if keyword != "" && strings.Contains(model, keyword) {
			return true
		}
	}
	return false
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
