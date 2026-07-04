package model

import (
	"fmt"
	"net/url"
	"strconv"
)

type SettingKey string

const (
	SettingKeyProxyURL                       SettingKey = "proxy_url"
	SettingKeyStatsSaveInterval              SettingKey = "stats_save_interval"                // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval        SettingKey = "model_info_update_interval"         // 模型信息更新间隔(小时)
	SettingKeySyncLLMInterval                SettingKey = "sync_llm_interval"                  // LLM 同步间隔(小时)
	SettingKeyRelayLogKeepPeriod             SettingKey = "relay_log_keep_period"              // 日志保存时间范围(天)
	SettingKeyRelayLogKeepEnabled            SettingKey = "relay_log_keep_enabled"             // 是否保留历史日志
	SettingKeyCORSAllowOrigins               SettingKey = "cors_allow_origins"                 // 跨域白名单(逗号分隔, 如 "example.com,example2.com"). 为空不允许跨域, "*"允许所有
	SettingKeyCircuitBreakerThreshold        SettingKey = "circuit_breaker_threshold"          // 熔断触发阈值（连续失败次数）
	SettingKeyCircuitBreakerCooldown         SettingKey = "circuit_breaker_cooldown"           // 熔断基础冷却时间（秒）
	SettingKeyCircuitBreakerMaxCooldown      SettingKey = "circuit_breaker_max_cooldown"       // 熔断最大冷却时间（秒），指数退避上限
	SettingKeySmartHealthEnabled             SettingKey = "smart_health_enabled"               // 是否启用智能健康系统
	SettingKeyHealthWeightedBalancerEnabled  SettingKey = "health_weighted_balancer_enabled"   // 是否启用健康权重参与加权调度
	SettingKeyHealthMinAdaptiveTimeout       SettingKey = "health_min_adaptive_timeout"        // 自动首字超时下限（秒）
	SettingKeyHealthSlowModelMinTimeout      SettingKey = "health_slow_model_min_timeout"      // 慢首字模型自动超时下限（秒）
	SettingKeyHealthRecoveryProbeEvery       SettingKey = "health_recovery_probe_every"        // 低健康候选恢复探测频率（每 N 次）
	SettingKeyHealthTimeoutRateThreshold     SettingKey = "health_timeout_rate_threshold"      // 自动超时率放宽阈值（百分比）
	SettingKeyStickyHealthyFirstTokenTimeout SettingKey = "sticky_healthy_first_token_timeout" // 粘性健康首token阈值（秒），0=关闭健康粘性检查
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},               // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},                  // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"},         // 默认24小时更新一次模型信息
		{Key: SettingKeySyncLLMInterval, Value: "24"},                 // 默认24小时同步一次LLM
		{Key: SettingKeyRelayLogKeepPeriod, Value: "7"},               // 默认日志保存7天
		{Key: SettingKeyRelayLogKeepEnabled, Value: "true"},           // 默认保留历史日志
		{Key: SettingKeyCircuitBreakerThreshold, Value: "5"},          // 默认连续失败5次触发熔断
		{Key: SettingKeyCircuitBreakerCooldown, Value: "60"},          // 默认基础冷却60秒
		{Key: SettingKeyCircuitBreakerMaxCooldown, Value: "600"},      // 默认最大冷却600秒（10分钟）
		{Key: SettingKeySmartHealthEnabled, Value: "true"},            // 默认启用智能健康系统
		{Key: SettingKeyHealthWeightedBalancerEnabled, Value: "true"}, // 默认启用健康权重参与加权调度
		{Key: SettingKeyHealthMinAdaptiveTimeout, Value: "15"},        // 自动首字超时不低于15秒
		{Key: SettingKeyHealthSlowModelMinTimeout, Value: "25"},       // thinking/opus等慢首字模型不低于25秒
		{Key: SettingKeyHealthRecoveryProbeEvery, Value: "20"},        // 低健康候选每20次评估探测一次
		{Key: SettingKeyHealthTimeoutRateThreshold, Value: "20"},      // 自动超时率>=20%时放宽超时
		{Key: SettingKeyStickyHealthyFirstTokenTimeout, Value: "0"},   // 默认关闭健康粘性检查（0=任何成功都粘住）
	}
}

func (s *Setting) Validate() error {
	switch s.Key {
	case SettingKeyModelInfoUpdateInterval, SettingKeySyncLLMInterval, SettingKeyRelayLogKeepPeriod,
		SettingKeyCircuitBreakerThreshold, SettingKeyCircuitBreakerCooldown, SettingKeyCircuitBreakerMaxCooldown,
		SettingKeyStickyHealthyFirstTokenTimeout, SettingKeyHealthMinAdaptiveTimeout, SettingKeyHealthSlowModelMinTimeout,
		SettingKeyHealthRecoveryProbeEvery, SettingKeyHealthTimeoutRateThreshold:
		_, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("model info update interval must be an integer")
		}
		return nil
	case SettingKeyRelayLogKeepEnabled, SettingKeySmartHealthEnabled, SettingKeyHealthWeightedBalancerEnabled:
		if s.Value != "true" && s.Value != "false" {
			return fmt.Errorf("%s must be true or false", s.Key)
		}
		return nil
	case SettingKeyProxyURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("proxy URL is invalid: %w", err)
		}
		validSchemes := map[string]bool{
			"http":   true,
			"https":  true,
			"socks5": true,
		}
		if !validSchemes[parsedURL.Scheme] {
			return fmt.Errorf("proxy URL scheme must be http, https, socks, or socks5")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("proxy URL must have a host")
		}
		return nil
	}

	return nil
}
