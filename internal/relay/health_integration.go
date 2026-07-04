package relay

import (
	"sync"
	"sync/atomic"

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

const healthRecoveryProbeEvery = 20

// InitHealthSystem 初始化健康系统
func InitHealthSystem(config health.HealthConfig) {
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

	if healthManager == nil {
		InitHealthSystem(health.DefaultHealthConfig())
	}
	if healthPersistence != nil {
		return nil
	}

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
	return atomic.AddUint64(&healthRecoveryProbeCounter, 1)%healthRecoveryProbeEvery == 0
}

func healthWeightedBalancerEnabled() bool {
	if !smartHealthEnabled() {
		return false
	}
	enabled, err := op.SettingGetBool(dbmodel.SettingKeyHealthWeightedBalancerEnabled)
	return err == nil && enabled
}

// init 默认初始化
func init() {
	// 使用默认配置初始化
	InitHealthSystem(health.DefaultHealthConfig())
}
