package health

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// HealthManager 健康管理器
// 管理所有渠道的健康状态，线程安全
type HealthManager struct {
	mu      sync.RWMutex
	states  map[HealthKey]*ChannelHealth
	config  HealthConfig
	enabled bool
}

// NewHealthManager 创建健康管理器
func NewHealthManager(config HealthConfig) *HealthManager {
	return &HealthManager{
		states:  make(map[HealthKey]*ChannelHealth),
		config:  config,
		enabled: true,
	}
}

// UpdateConfig applies runtime health settings without discarding accumulated
// latency and success-rate evidence. Existing ChannelHealth instances keep
// their estimators and counters while adopting the new policy immediately.
func (m *HealthManager) UpdateConfig(config HealthConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = config
	for _, channelHealth := range m.states {
		channelHealth.mu.Lock()
		channelHealth.Config = config
		if config.WindowSize > 0 && len(channelHealth.Stats.RecentResults) > config.WindowSize {
			channelHealth.Stats.RecentResults = append([]bool(nil), channelHealth.Stats.RecentResults[len(channelHealth.Stats.RecentResults)-config.WindowSize:]...)
		}
		channelHealth.recomputeLocked()
		channelHealth.mu.Unlock()
	}
}

// GetOrCreate 获取或创建渠道健康状态
func (m *HealthManager) GetOrCreate(key HealthKey) *ChannelHealth {
	// 快速路径：只读锁
	m.mu.RLock()
	if health, ok := m.states[key]; ok {
		m.mu.RUnlock()
		return health
	}
	m.mu.RUnlock()

	// 慢速路径：写锁
	m.mu.Lock()
	defer m.mu.Unlock()

	// 双重检查，避免重复创建
	if health, ok := m.states[key]; ok {
		return health
	}

	// 创建新状态
	health := NewChannelHealth(key, m.config)
	m.states[key] = health

	return health
}

// Get 获取渠道健康状态（只读）
func (m *HealthManager) Get(key HealthKey) (*ChannelHealth, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	health, ok := m.states[key]
	return health, ok
}

// RecordSuccess 记录成功事件
func (m *HealthManager) RecordSuccess(
	channelID int,
	keyID int,
	model string,
	firstTokenTime time.Duration,
) {
	if !m.enabled {
		return
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health := m.GetOrCreate(key)

	event := HealthEvent{
		Level:          errorclass.ErrorLevelNone,
		Outcome:        OutcomeSuccess,
		FirstTokenTime: firstTokenTime,
		At:             time.Now(),
	}

	health.OnEvent(event)
}

// RecordError 记录错误事件
func (m *HealthManager) RecordError(
	channelID int,
	keyID int,
	model string,
	classification *errorclass.Classification,
	statusCode int,
	transportErr error,
	firstTokenTime time.Duration,
) {
	if !m.enabled {
		return
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health := m.GetOrCreate(key)

	// 处理 transport error（没有 classification）
	if transportErr != nil {
		level := levelFromTransportError(transportErr)
		outcome := outcomeFromTransportError(transportErr)

		event := HealthEvent{
			Level:          level,
			Outcome:        outcome,
			HTTPStatus:     0,
			FirstTokenTime: firstTokenTime,
			At:             time.Now(),
		}

		// 客户端主动取消，不惩罚渠道
		if level == errorclass.ErrorLevelClient {
			return
		}

		health.OnEvent(event)
		return
	}

	if classification == nil {
		classification = &errorclass.Classification{Level: errorclass.ErrorLevelChannel, Reason: "missing error classification"}
	}

	// 处理 HTTP 响应错误
	outcome := mapToOutcome(classification, statusCode)

	event := HealthEvent{
		Level:          classification.Level,
		Outcome:        outcome,
		HTTPStatus:     statusCode,
		FirstTokenTime: firstTokenTime,
		At:             time.Now(),
	}

	// 根据 Level 决定是否惩罚渠道
	switch classification.Level {
	case errorclass.ErrorLevelClient:
		// 客户端错误，不惩罚渠道
		return

	case errorclass.ErrorLevelKey:
		// Key 级错误，记录但不降低渠道健康度
		health.mu.Lock()
		health.Stats.KeyErrorCount++
		health.mu.Unlock()
		return

	case errorclass.ErrorLevelChannel:
		// 渠道级错误，降低健康度
		health.OnEvent(event)

	case errorclass.ErrorLevelNone:
		// 成功（不应该走到这里）
		health.OnEvent(event)
	}
}

// RecordTimeout 记录超时事件
func (m *HealthManager) RecordTimeout(
	channelID int,
	keyID int,
	model string,
	timeoutBudget time.Duration,
) {
	if !m.enabled {
		return
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health := m.GetOrCreate(key)

	event := HealthEvent{
		Level:         errorclass.ErrorLevelChannel,
		Outcome:       OutcomeFirstTokenTimeout,
		TimeoutBudget: timeoutBudget,
		At:            time.Now(),
	}

	health.OnEvent(event)
}

// GetTimeout 获取自适应超时
func (m *HealthManager) GetTimeout(channelID int, keyID int, model string) time.Duration {
	if !m.enabled {
		return m.config.DefaultTimeout
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health, ok := m.Get(key)
	if !ok {
		// 新渠道，使用冷启动超时
		return m.config.ColdStartTimeout
	}

	return health.GetTimeout()
}

// HasAdaptiveTimeout reports whether this key has enough observations to safely
// enforce an adaptive first-token timeout. Missing or cold states should not add
// a new timeout guard because older group config used 0 to mean no first-token
// limit.
func (m *HealthManager) HasAdaptiveTimeout(channelID int, keyID int, model string) bool {
	if !m.enabled {
		return false
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health, ok := m.Get(key)
	if !ok {
		return false
	}

	stats := health.GetStats()
	return stats.TotalCount >= int64(m.config.MinSamplesForAdaptiveTimeout)
}

// GetScore 获取健康度评分
func (m *HealthManager) GetScore(channelID int, keyID int, model string) float64 {
	if !m.enabled {
		return 1.0
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health, ok := m.Get(key)
	if !ok {
		// 新渠道，默认满分
		return 1.0
	}

	return health.GetScore()
}

// GetAllStates 获取所有健康状态（只读副本）
func (m *HealthManager) GetAllStates() map[HealthKey]HealthStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[HealthKey]HealthStats, len(m.states))
	for key, health := range m.states {
		result[key] = health.GetStats()
	}

	return result
}

// Enable 启用健康系统
func (m *HealthManager) Enable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = true
}

// Disable 禁用健康系统
func (m *HealthManager) Disable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = false
}

// IsEnabled 检查是否启用
func (m *HealthManager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// Cleanup 清理过期状态
func (m *HealthManager) Cleanup(ctx context.Context, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for key, health := range m.states {
		stats := health.GetStats()
		if now.Sub(stats.LastEventAt) > ttl {
			delete(m.states, key)
		}
	}
}

// levelFromTransportError 从传输层错误映射到 ErrorLevel
func levelFromTransportError(err error) errorclass.ErrorLevel {
	if err == nil {
		return errorclass.ErrorLevelNone
	}

	// context.Canceled 是客户端主动取消
	if err == context.Canceled {
		return errorclass.ErrorLevelClient
	}

	// 其他传输错误属于渠道级
	return errorclass.ErrorLevelChannel
}

// outcomeFromTransportError 从传输层错误映射到 Outcome
func outcomeFromTransportError(err error) HealthOutcome {
	if err == nil {
		return OutcomeSuccess
	}

	if err == context.Canceled {
		return OutcomeClientCancel
	}

	if strings.Contains(err.Error(), "first token timeout") {
		return OutcomeFirstTokenTimeout
	}

	return OutcomeNetworkError
}

// mapToOutcome 从 classification 映射到 outcome
func mapToOutcome(
	classification *errorclass.Classification,
	statusCode int,
) HealthOutcome {
	if classification == nil {
		return OutcomeUpstreamError
	}

	// 成功
	if classification.Level == errorclass.ErrorLevelNone {
		return OutcomeSuccess
	}

	// 根据 Reason 精确匹配（基于 classifier.go 实际输出）
	reason := classification.Reason

	if strings.HasPrefix(reason, "429 rate limit") {
		return OutcomeRateLimit
	}

	// 404 model_not_found - 客户端配置错误
	if reason == "404 model_not_found" {
		return OutcomeClientError
	}

	// 404 其他 - 渠道级
	if strings.HasPrefix(reason, "404") {
		return OutcomeUpstreamError
	}

	// 400 quota/billing - Key 级限流
	if strings.HasPrefix(reason, "quota") || strings.HasPrefix(reason, "billing") {
		return OutcomeRateLimit
	}

	// 400 bad request - 客户端错误
	if reason == "400 bad request" {
		return OutcomeClientError
	}

	// 503 model permission - Key 级权限问题
	if strings.HasPrefix(reason, "503 model permission") {
		return OutcomeRateLimit
	}

	// 503 其他 - 渠道级
	if strings.HasPrefix(reason, "503") {
		return OutcomeUpstreamError
	}

	// 499 - 上游客户端关闭
	if statusCode == 499 {
		return OutcomeUpstreamError
	}

	// 5xx 服务器错误
	if statusCode >= 500 {
		return OutcomeUpstreamError
	}

	// 默认：上游错误
	return OutcomeUpstreamError
}

// IsShadowMode 检查是否开启 shadow mode
func (m *HealthManager) IsShadowMode() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.ShadowMode
}

// RecordShadowTimeout 记录 shadow 模式下的超时事件（只统计不执行）
func (m *HealthManager) RecordShadowTimeout(channelID int, keyID int, model string) {
	if !m.enabled {
		return
	}

	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health := m.GetOrCreate(key)
	health.RecordShadowTimeout()
}
