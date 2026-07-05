package relay

import (
	"strconv"
	"strings"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/health"
)

// HealthPolicy is the relay-facing snapshot of health-related settings.
// It centralizes setting reads so request routing code does not grow new direct
// dependencies on individual health setting keys.
type HealthPolicy struct {
	SmartHealthEnabled    bool
	WeightedBalancer      bool
	RecoveryProbeEvery    int
	RecoveryProbeInterval time.Duration
}

func currentHealthPolicy() HealthPolicy {
	return loadHealthPolicy()
}

func loadHealthPolicy() HealthPolicy {
	policy := HealthPolicy{
		SmartHealthEnabled:    settingBool(dbmodel.SettingKeySmartHealthEnabled, false),
		RecoveryProbeEvery:    settingInt(dbmodel.SettingKeyHealthRecoveryProbeEvery, 20),
		RecoveryProbeInterval: time.Duration(settingInt(dbmodel.SettingKeyHealthRecoveryProbeInterval, 300)) * time.Second,
	}
	if policy.SmartHealthEnabled {
		policy.WeightedBalancer = settingBool(dbmodel.SettingKeyHealthWeightedBalancerEnabled, false)
	}
	if policy.RecoveryProbeEvery < 0 {
		policy.RecoveryProbeEvery = 0
	}
	if policy.RecoveryProbeInterval < 0 {
		policy.RecoveryProbeInterval = 0
	}
	return policy
}

func applyHealthSettings(config health.HealthConfig) health.HealthConfig {
	return applyHealthConfigSettings(config)
}

func smartHealthEnabled() bool {
	return currentHealthPolicy().SmartHealthEnabled
}

func healthWeightedBalancerEnabled() bool {
	return currentHealthPolicy().WeightedBalancer
}

func healthRecoveryProbeEvery() int {
	return currentHealthPolicy().RecoveryProbeEvery
}

func healthRecoveryProbeInterval() time.Duration {
	return currentHealthPolicy().RecoveryProbeInterval
}

func applyHealthConfigSettings(config health.HealthConfig) health.HealthConfig {
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
		if floatVal, parseErr := strconv.ParseFloat(value, 64); parseErr == nil && floatVal > 0 {
			config.MaxMultiplierStack = floatVal
		}
	}
	return config
}

func settingBool(key dbmodel.SettingKey, fallback bool) bool {
	value, err := op.SettingGetBool(key)
	if err != nil {
		return fallback
	}
	return value
}

func settingInt(key dbmodel.SettingKey, fallback int) int {
	value, err := op.SettingGetInt(key)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
