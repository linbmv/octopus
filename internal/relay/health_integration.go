package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/metrics"
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

// ReloadHealthSettings hot-applies persisted health policy changes while
// retaining runtime samples and persistence ownership.
func ReloadHealthSettings() {
	if healthManager == nil {
		return
	}
	healthManager.UpdateConfig(applyHealthSettings(health.DefaultHealthConfig()))
}

// RefreshHealthMetrics 刷新 Prometheus 指标快照。
func RefreshHealthMetrics() {
	if healthMetrics == nil || healthManager == nil {
		return
	}
	healthMetrics.UpdateAll(healthManager)
}

// RefreshCircuitMetrics 把熔断器各状态的条目数刷新到 Prometheus gauge，
// 在 /metrics 抓取时随健康快照一起调用。
func RefreshCircuitMetrics() {
	closed, open, halfOpen := balancer.CircuitStateCounts()
	metrics.CircuitBreakerEntries.WithLabelValues("closed").Set(float64(closed))
	metrics.CircuitBreakerEntries.WithLabelValues("open").Set(float64(open))
	metrics.CircuitBreakerEntries.WithLabelValues("half_open").Set(float64(halfOpen))
}

func StartHealthPersistenceContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := persistence.Load(); err != nil {
		log.Warnf("failed to load health states: %v", err)
	}
	if err := persistence.StartContext(ctx); err != nil {
		return err
	}
	healthPersistence = persistence
	return nil
}

func StopHealthPersistenceContext(ctx context.Context) error {
	healthPersistenceMu.Lock()
	persistence := healthPersistence
	healthPersistence = nil
	healthPersistenceMu.Unlock()

	if persistence != nil {
		return persistence.StopContext(ctx)
	}
	return nil
}

type HealthPersistenceWorker struct{}

func DefaultHealthPersistenceWorker() HealthPersistenceWorker { return HealthPersistenceWorker{} }

func (HealthPersistenceWorker) Start(ctx context.Context) error {
	return StartHealthPersistenceContext(ctx)
}

func (HealthPersistenceWorker) Stop(ctx context.Context) error {
	return StopHealthPersistenceContext(ctx)
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

// init 默认初始化
func init() {
	// 使用默认配置初始化
	InitHealthSystem(health.DefaultHealthConfig())
}
