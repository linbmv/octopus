package relay

import (
	"github.com/bestruirui/octopus/internal/relay/health"
)

// 全局健康管理器
var healthManager *health.HealthManager

// InitHealthSystem 初始化健康系统
func InitHealthSystem(config health.HealthConfig) {
	healthManager = health.NewHealthManager(config)
}

// GetHealthManager 获取健康管理器
func GetHealthManager() *health.HealthManager {
	return healthManager
}

// init 默认初始化
func init() {
	// 使用默认配置初始化
	InitHealthSystem(health.DefaultHealthConfig())
}
