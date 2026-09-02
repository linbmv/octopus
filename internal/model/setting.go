package model

import (
	"fmt"
	"net/url"
	"strconv"
)

type SettingKey string

const (
	SettingKeyProxyURL                     SettingKey = "proxy_url"
	SettingKeyStatsSaveInterval            SettingKey = "stats_save_interval"              // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval      SettingKey = "model_info_update_interval"       // 模型信息更新间隔(小时)
	SettingKeySyncLLMInterval              SettingKey = "sync_llm_interval"                // LLM 同步间隔(小时)
	SettingKeyCORSAllowOrigins             SettingKey = "cors_allow_origins"               // 跨域白名单(逗号分隔, 如 "example.com,example2.com"). 为空不允许跨域, "*"允许所有
	SettingKeyJWTSecret                    SettingKey = "jwt_secret"                       // 独立随机生成的 JWT 签名密钥(base64)，首次启动写入。
	SettingKeyTokenVersion                 SettingKey = "token_version"                    // 会话版本；改密/改用户名时递增，使已签发 token 立即失效。
	SettingKeyCircuitBreakerThreshold      SettingKey = "circuit_breaker_threshold"        // 熔断触发阈值（连续失败次数）。
	SettingKeyCircuitBreakerCooldown       SettingKey = "circuit_breaker_cooldown"         // 熔断基础冷却时间（秒）。
	SettingKeyCircuitBreakerMaxCooldown    SettingKey = "circuit_breaker_max_cooldown"     // 熔断最大冷却时间（秒）。
	SettingKeyCircuitBreakerHalfOpenProbes SettingKey = "circuit_breaker_half_open_probes" // 半开态并发试探数上限。
	SettingKeyCircuitBreakerProbeLease     SettingKey = "circuit_breaker_probe_lease"      // 半开试探租约（秒）。
)

// IsSecret 标记不可通过设置接口读写、也不可进入备份的敏感项。
// 签名密钥与会话版本属于运行时凭据状态：前者泄露即可伪造任意 admin token，
// 后者被回退会让本应失效的旧 token 重新可用。
func (k SettingKey) IsSecret() bool {
	switch k {
	case SettingKeyJWTSecret, SettingKeyTokenVersion:
		return true
	}
	return false
}

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},           // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},              // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"},     // 默认24小时更新一次模型信息
		{Key: SettingKeySyncLLMInterval, Value: "24"},             // 默认24小时同步一次LLM
		{Key: SettingKeyCircuitBreakerThreshold, Value: "2"},      // 默认连续失败2次触发熔断
		{Key: SettingKeyCircuitBreakerCooldown, Value: "60"},      // 默认基础冷却60秒
		{Key: SettingKeyCircuitBreakerMaxCooldown, Value: "600"},  // 默认最大冷却600秒
		{Key: SettingKeyCircuitBreakerHalfOpenProbes, Value: "2"}, // 默认允许2个并发试探
		{Key: SettingKeyCircuitBreakerProbeLease, Value: "60"},    // 默认试探租约60秒
	}
}

func (s *Setting) Validate() error {
	switch s.Key {
	case SettingKeyModelInfoUpdateInterval, SettingKeySyncLLMInterval:
		_, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("model info update interval must be an integer")
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
	case SettingKeyCircuitBreakerThreshold, SettingKeyCircuitBreakerCooldown,
		SettingKeyCircuitBreakerMaxCooldown, SettingKeyCircuitBreakerProbeLease:
		value, err := strconv.Atoi(s.Value)
		if err != nil || value < 1 {
			return fmt.Errorf("%s must be a positive integer", s.Key)
		}
		return nil
	case SettingKeyCircuitBreakerHalfOpenProbes:
		value, err := strconv.Atoi(s.Value)
		if err != nil || value < 1 || value > 64 {
			return fmt.Errorf("%s must be an integer between 1 and 64", s.Key)
		}
	}

	return nil
}
