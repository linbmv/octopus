package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
)

type SettingKey string

const (
	SettingKeyProxyURL                       SettingKey = "proxy_url"
	SettingKeyStatsSaveInterval              SettingKey = "stats_save_interval"                // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval        SettingKey = "model_info_update_interval"         // 模型信息更新间隔(小时)
	SettingKeySyncLLMInterval                SettingKey = "sync_llm_interval"                  // LLM 同步间隔(小时)
	SettingKeyRelayLogKeepPeriod             SettingKey = "relay_log_keep_period"              // 日志保存时间范围(天)
	SettingKeyRelayLogKeepEnabled            SettingKey = "relay_log_keep_enabled"             // 是否保留历史日志
	SettingKeyRelayLogContentMode            SettingKey = "relay_log_content_mode"             // Relay 日志内容策略(metadata/full/disabled)
	SettingKeyCORSAllowOrigins               SettingKey = "cors_allow_origins"                 // 跨域白名单(逗号分隔, 如 "example.com,example2.com"). 为空不允许跨域, "*"允许所有
	SettingKeyCircuitBreakerThreshold        SettingKey = "circuit_breaker_threshold"          // 熔断触发阈值（连续失败次数）
	SettingKeyCircuitBreakerCooldown         SettingKey = "circuit_breaker_cooldown"           // 熔断基础冷却时间（秒）
	SettingKeyCircuitBreakerMaxCooldown      SettingKey = "circuit_breaker_max_cooldown"       // 熔断最大冷却时间（秒），指数退避上限
	SettingKeyCircuitBreakerHalfOpenProbes   SettingKey = "circuit_breaker_half_open_probes"   // 半开态并发试探数上限
	SettingKeyCircuitBreakerProbeLease       SettingKey = "circuit_breaker_probe_lease"        // 半开试探租约（秒）：在途试探超时未结算则放行新试探
	SettingKeySmartHealthEnabled             SettingKey = "smart_health_enabled"               // 是否启用智能健康系统
	SettingKeyHealthWeightedBalancerEnabled  SettingKey = "health_weighted_balancer_enabled"   // 是否启用健康权重参与加权调度
	SettingKeyHealthMinAdaptiveTimeout       SettingKey = "health_min_adaptive_timeout"        // 自动首字超时下限（秒）
	SettingKeyHealthSlowModelMinTimeout      SettingKey = "health_slow_model_min_timeout"      // 慢首字模型自动超时下限（秒）
	SettingKeyHealthRecoveryProbeEvery       SettingKey = "health_recovery_probe_every"        // 低健康候选恢复探测频率（每 N 次）
	SettingKeyHealthRecoveryProbeInterval    SettingKey = "health_recovery_probe_interval"     // 低健康候选恢复探测最小间隔（秒）
	SettingKeyHealthTimeoutRateThreshold     SettingKey = "health_timeout_rate_threshold"      // 自动超时率放宽阈值（百分比）
	SettingKeyHealthSlowModelKeywords        SettingKey = "health_slow_model_keywords"         // 慢首字模型关键词（逗号分隔）
	SettingKeyHealthShadowMode               SettingKey = "health_shadow_mode"                 // 自动超时 shadow 模式（只记录不执行）
	SettingKeyHealthMaxMultiplierStack       SettingKey = "health_max_multiplier_stack"        // multiplier 叠加上限（浮点数，0=无限制）
	SettingKeyStickyHealthyFirstTokenTimeout SettingKey = "sticky_healthy_first_token_timeout" // 粘性健康首token阈值（秒），0=关闭健康粘性检查
	SettingKeyChannelCardPinnedIDs           SettingKey = "channel_card_pinned_ids"            // 全局渠道卡片置顶 ID 列表（JSON 数组）
	SettingKeyGroupCardPinnedIDs             SettingKey = "group_card_pinned_ids"              // 全局分组卡片置顶 ID 列表（JSON 数组）
	SettingKeyChannelCardOrderedIDs          SettingKey = "channel_card_ordered_ids"           // 全局渠道卡片手动排序 ID 列表（JSON 数组）
	SettingKeyGroupCardOrderedIDs            SettingKey = "group_card_ordered_ids"             // 全局分组卡片手动排序 ID 列表（JSON 数组）
	SettingKeyChannelCardSortMode            SettingKey = "channel_card_sort_mode"             // 全局渠道卡片排序模式
	SettingKeyGroupCardSortMode              SettingKey = "group_card_sort_mode"               // 全局分组卡片排序模式
)

type RelayLogContentMode string

const (
	RelayLogContentModeMetadata RelayLogContentMode = "metadata"
	RelayLogContentModeFull     RelayLogContentMode = "full"
	RelayLogContentModeDisabled RelayLogContentMode = "disabled"
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

type settingSchema struct {
	validate func(string) error
}

const maxDurationValue = int64(^uint64(0) >> 1)

var settingSchemas = map[SettingKey]settingSchema{
	SettingKeyProxyURL:                       {validate: validateProxyURL},
	SettingKeyStatsSaveInterval:              {validate: validateIntegerRange(0, maxDurationValue/int64(time.Minute), "minutes")},
	SettingKeyModelInfoUpdateInterval:        {validate: validateIntegerRange(0, maxDurationValue/int64(time.Hour), "hours")},
	SettingKeySyncLLMInterval:                {validate: validateIntegerRange(0, maxDurationValue/int64(time.Hour), "hours")},
	SettingKeyRelayLogKeepPeriod:             {validate: validateIntegerRange(0, maxDurationValue/int64(24*time.Hour), "days")},
	SettingKeyRelayLogKeepEnabled:            {validate: validateBoolean},
	SettingKeyRelayLogContentMode:            {validate: validateRelayLogContentMode},
	SettingKeyCORSAllowOrigins:               {validate: validateString},
	SettingKeyCircuitBreakerThreshold:        {validate: validateIntegerRange(1, math.MaxInt32, "failures")},
	SettingKeyCircuitBreakerCooldown:         {validate: validateIntegerRange(1, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyCircuitBreakerMaxCooldown:      {validate: validateIntegerRange(1, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyCircuitBreakerHalfOpenProbes:   {validate: validateIntegerRange(1, 64, "probes")},
	SettingKeyCircuitBreakerProbeLease:       {validate: validateIntegerRange(1, 86400, "seconds")},
	SettingKeySmartHealthEnabled:             {validate: validateBoolean},
	SettingKeyHealthWeightedBalancerEnabled:  {validate: validateBoolean},
	SettingKeyHealthMinAdaptiveTimeout:       {validate: validateIntegerRange(1, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyHealthSlowModelMinTimeout:      {validate: validateIntegerRange(1, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyHealthRecoveryProbeEvery:       {validate: validateIntegerRange(0, math.MaxInt32, "evaluations")},
	SettingKeyHealthRecoveryProbeInterval:    {validate: validateIntegerRange(0, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyHealthTimeoutRateThreshold:     {validate: validateIntegerRange(0, 100, "percent")},
	SettingKeyHealthSlowModelKeywords:        {validate: validateString},
	SettingKeyHealthShadowMode:               {validate: validateBoolean},
	SettingKeyHealthMaxMultiplierStack:       {validate: validateNonNegativeFloat},
	SettingKeyStickyHealthyFirstTokenTimeout: {validate: validateIntegerRange(0, maxDurationValue/int64(time.Second), "seconds")},
	SettingKeyChannelCardPinnedIDs:           {validate: validateCardIDs},
	SettingKeyGroupCardPinnedIDs:             {validate: validateCardIDs},
	SettingKeyChannelCardOrderedIDs:          {validate: validateCardIDs},
	SettingKeyGroupCardOrderedIDs:            {validate: validateCardIDs},
	SettingKeyChannelCardSortMode:            {validate: validateCardSortMode},
	SettingKeyGroupCardSortMode:              {validate: validateCardSortMode},
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},                                  // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},                                     // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"},                            // 默认24小时更新一次模型信息
		{Key: SettingKeySyncLLMInterval, Value: "24"},                                    // 默认24小时同步一次LLM
		{Key: SettingKeyRelayLogKeepPeriod, Value: "7"},                                  // 默认日志保存7天
		{Key: SettingKeyRelayLogKeepEnabled, Value: "true"},                              // 默认保留历史日志
		{Key: SettingKeyRelayLogContentMode, Value: string(RelayLogContentModeMetadata)}, // 默认仅保存非敏感元数据
		{Key: SettingKeyCircuitBreakerThreshold, Value: "2"},                             // 默认连续失败2次触发熔断
		{Key: SettingKeyCircuitBreakerCooldown, Value: "60"},                             // 默认基础冷却60秒
		{Key: SettingKeyCircuitBreakerMaxCooldown, Value: "600"},                         // 默认最大冷却600秒（10分钟）
		{Key: SettingKeyCircuitBreakerHalfOpenProbes, Value: "2"},                        // 半开态默认允许2个并发试探
		{Key: SettingKeyCircuitBreakerProbeLease, Value: "60"},                           // 试探租约默认60秒，超时未结算即放行新试探
		{Key: SettingKeySmartHealthEnabled, Value: "true"},                               // 默认启用智能健康系统
		{Key: SettingKeyHealthWeightedBalancerEnabled, Value: "false"},                   // 默认关闭健康度参与调度，显式启用后才改变候选顺序
		{Key: SettingKeyHealthMinAdaptiveTimeout, Value: "15"},                           // 自动首字超时不低于15秒
		{Key: SettingKeyHealthSlowModelMinTimeout, Value: "25"},                          // thinking/opus等慢首字模型不低于25秒
		{Key: SettingKeyHealthRecoveryProbeEvery, Value: "20"},                           // 低健康候选每20次评估探测一次
		{Key: SettingKeyHealthRecoveryProbeInterval, Value: "300"},                       // 或每5分钟至少探测一次
		{Key: SettingKeyHealthTimeoutRateThreshold, Value: "20"},                         // 自动超时率>=20%时放宽超时
		{Key: SettingKeyHealthSlowModelKeywords, Value: "thinking,opus,reasoning,long-context,long_context,200k,1m"},
		{Key: SettingKeyHealthShadowMode, Value: "false"},           // shadow mode 默认关闭
		{Key: SettingKeyHealthMaxMultiplierStack, Value: "3.0"},     // multiplier 叠加上限 3.0x
		{Key: SettingKeyStickyHealthyFirstTokenTimeout, Value: "0"}, // 默认不启用粘性健康检查
		{Key: SettingKeyChannelCardPinnedIDs, Value: "[]"},          // 默认没有全局置顶渠道卡片
		{Key: SettingKeyGroupCardPinnedIDs, Value: "[]"},            // 默认没有全局置顶分组卡片
		{Key: SettingKeyChannelCardOrderedIDs, Value: "[]"},         // 默认没有渠道卡片手动顺序
		{Key: SettingKeyGroupCardOrderedIDs, Value: "[]"},           // 默认没有分组卡片手动顺序
		{Key: SettingKeyChannelCardSortMode, Value: "name-asc"},     // 默认按渠道名称正序
		{Key: SettingKeyGroupCardSortMode, Value: "name-asc"},       // 默认按分组名称正序
	}
}

func (s *Setting) Validate() error {
	schema, ok := settingSchemas[s.Key]
	if !ok {
		return fmt.Errorf("unknown setting key %q", s.Key)
	}
	if err := schema.validate(s.Value); err != nil {
		return fmt.Errorf("%s: %w", s.Key, err)
	}
	return nil
}

func validateIntegerRange(minValue, maxValue int64, unit string) func(string) error {
	return func(value string) error {
		parsed, err := strconv.ParseInt(value, 10, 0)
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		if parsed < minValue || parsed > maxValue {
			return fmt.Errorf("must be between %d and %d %s", minValue, maxValue, unit)
		}
		return nil
	}
}

func validateBoolean(value string) error {
	if value != "true" && value != "false" {
		return fmt.Errorf("must be true or false")
	}
	return nil
}

func ParseRelayLogContentMode(value string) (RelayLogContentMode, error) {
	mode := RelayLogContentMode(value)
	switch mode {
	case RelayLogContentModeMetadata, RelayLogContentModeFull, RelayLogContentModeDisabled:
		return mode, nil
	default:
		return "", fmt.Errorf("must be one of metadata, full, or disabled")
	}
}

func validateRelayLogContentMode(value string) error {
	_, err := ParseRelayLogContentMode(value)
	return err
}

func validateString(string) error {
	return nil
}

func validateCardIDs(value string) error {
	trimmed := bytes.TrimSpace([]byte(value))
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("must be a JSON array of positive IDs")
	}
	var ids []int
	if err := json.Unmarshal([]byte(value), &ids); err != nil {
		return fmt.Errorf("must be a JSON array of positive IDs")
	}
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("IDs must be positive")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateCardSortMode(value string) error {
	switch value {
	case "name-asc", "name-desc", "created-asc", "created-desc", "manual":
		return nil
	default:
		return fmt.Errorf("must be one of name-asc, name-desc, created-asc, created-desc, or manual")
	}
}

func validateNonNegativeFloat(value string) error {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return fmt.Errorf("must be a finite number")
	}
	if parsed < 0 {
		return fmt.Errorf("must be non-negative (0 = no limit)")
	}
	return nil
}

func validateProxyURL(value string) error {
	if value == "" {
		return nil
	}
	parsedURL, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("proxy URL is invalid: %w", err)
	}
	validSchemes := map[string]bool{
		"http":   true,
		"https":  true,
		"socks":  true,
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
