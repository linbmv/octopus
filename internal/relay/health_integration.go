package relay

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/health"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// 全局健康管理器
var healthManager *health.HealthManager
var healthMetrics *health.HealthMetrics
var healthMetricsOnce sync.Once
var healthPersistence *health.HealthPersistence
var healthPersistenceMu sync.Mutex
var healthRecoveryProbeCounter uint64
var healthLastRecoveryProbeUnix atomic.Int64

// InitHealthSystem 初始化健康系统
func InitHealthSystem(config health.HealthConfig) {
	config = applyHealthSettings(config)
	healthManager = health.NewHealthManager(config)
	healthMetricsOnce.Do(func() {
		healthMetrics = health.NewHealthMetrics("octopus")
	})
	balancer.SetHealthWeightFunc(healthWeightForGroupItem)
}

// GetHealthManager 获取健康管理器
func GetHealthManager() *health.HealthManager {
	return healthManager
}

// GetHealthMetrics 获取健康指标管理器
func GetHealthMetrics() *health.HealthMetrics {
	return healthMetrics
}

// RefreshHealthMetrics 刷新 Prometheus 指标快照。
func RefreshHealthMetrics() {
	if healthMetrics == nil || healthManager == nil {
		return
	}
	healthMetrics.UpdateAll(healthManager)
}

// StartHealthPersistence 加载最近快照并启动周期性健康状态持久化。
func StartHealthPersistence() error {
	healthPersistenceMu.Lock()
	defer healthPersistenceMu.Unlock()

	if healthPersistence != nil {
		return nil
	}
	InitHealthSystem(health.DefaultHealthConfig())

	persistence, err := health.NewHealthPersistence(health.DefaultPersistenceConfig(), healthManager)
	if err != nil {
		return err
	}
	if persistence == nil {
		return nil
	}
	if err := persistence.Load(); err != nil {
		log.Warnf("failed to load health states: %v", err)
	}
	persistence.Start()
	healthPersistence = persistence
	return nil
}

// StopHealthPersistence 停止健康状态持久化并保存最后快照。
func StopHealthPersistence() error {
	healthPersistenceMu.Lock()
	persistence := healthPersistence
	healthPersistence = nil
	healthPersistenceMu.Unlock()

	if persistence != nil {
		persistence.Stop()
	}
	return nil
}

func healthWeightForGroupItem(item dbmodel.GroupItem) float64 {
	if item.Type == dbmodel.GroupItemTypeGroup || !healthWeightedBalancerEnabled() || healthManager == nil {
		return 1
	}
	channel, err := op.ChannelGet(item.ChannelID, nil)
	if err != nil || channel == nil {
		return 1
	}
	keys := channel.AvailableKeys()
	if len(keys) == 0 {
		return 1
	}
	bestScore := 0.0
	for _, key := range keys {
		score := healthManager.GetScore(item.ChannelID, key.ID, item.ModelName)
		if score > bestScore {
			bestScore = score
		}
	}
	if shouldProbeUnhealthyCandidate(bestScore) {
		return 1
	}
	if bestScore <= 0 {
		return 0.01
	}
	return bestScore
}

func shouldProbeUnhealthyCandidate(score float64) bool {
	if score <= 0 || score >= 0.5 {
		return false
	}
	probeEvery := healthRecoveryProbeEvery()
	if probeEvery > 0 && atomic.AddUint64(&healthRecoveryProbeCounter, 1)%uint64(probeEvery) == 0 {
		healthLastRecoveryProbeUnix.Store(time.Now().Unix())
		return true
	}
	probeInterval := healthRecoveryProbeInterval()
	if probeInterval <= 0 {
		return false
	}
	now := time.Now().Unix()
	last := healthLastRecoveryProbeUnix.Load()
	if last > 0 && now-last < int64(probeInterval/time.Second) {
		return false
	}
	return healthLastRecoveryProbeUnix.CompareAndSwap(last, now)
}

func healthRecoveryProbeEvery() int {
	value, err := op.SettingGetInt(dbmodel.SettingKeyHealthRecoveryProbeEvery)
	if err != nil || value <= 0 {
		return 20
	}
	return value
}

func healthRecoveryProbeInterval() time.Duration {
	value, err := op.SettingGetInt(dbmodel.SettingKeyHealthRecoveryProbeInterval)
	if err != nil || value <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(value) * time.Second
}

func healthWeightedBalancerEnabled() bool {
	if !smartHealthEnabled() {
		return false
	}
	enabled, err := op.SettingGetBool(dbmodel.SettingKeyHealthWeightedBalancerEnabled)
	return err == nil && enabled
}

func applyHealthSettings(config health.HealthConfig) health.HealthConfig {
	if value, err := op.SettingGetInt(dbmodel.SettingKeyHealthMinAdaptiveTimeout); err == nil && value > 0 {
		config.MinAdaptiveTimeout = time.Duration(value) * time.Second
	}
	if value, err := op.SettingGetInt(dbmodel.SettingKeyHealthSlowModelMinTimeout); err == nil && value > 0 {
		config.SlowModelMinAdaptiveTimeout = time.Duration(value) * time.Second
	}
	if value, err := op.SettingGetInt(dbmodel.SettingKeyHealthTimeoutRateThreshold); err == nil && value > 0 {
		config.TimeoutRateBackoffThreshold = float64(value) / 100
	}
	if value, err := op.SettingGetString(dbmodel.SettingKeyHealthSlowModelKeywords); err == nil {
		keywords := strings.Split(value, ",")
		for i := range keywords {
			keywords[i] = strings.TrimSpace(keywords[i])
		}
		config.SlowModelKeywords = keywords
	}
	if value, err := op.SettingGetBool(dbmodel.SettingKeyHealthShadowMode); err == nil {
		config.ShadowMode = value
	}
	if value, err := op.SettingGetString(dbmodel.SettingKeyHealthMaxMultiplierStack); err == nil {
		if floatVal, parseErr := parseFloat(value); parseErr == nil && floatVal > 0 {
			config.MaxMultiplierStack = floatVal
		}
	}
	return config
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// init 默认初始化
func init() {
	// 使用默认配置初始化
	InitHealthSystem(health.DefaultHealthConfig())
}
