package model

import "testing"

func TestSettingValidateHealthWeightedBalancerEnabled(t *testing.T) {
	valid := Setting{Key: SettingKeyHealthWeightedBalancerEnabled, Value: "true"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	invalid := Setting{Key: SettingKeyHealthWeightedBalancerEnabled, Value: "yes"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestDefaultSettingsIncludeHealthWeightedBalancerEnabled(t *testing.T) {
	want := map[SettingKey]string{
		SettingKeyHealthWeightedBalancerEnabled: "true",
		SettingKeyHealthMinAdaptiveTimeout:      "15",
		SettingKeyHealthSlowModelMinTimeout:     "25",
		SettingKeyHealthRecoveryProbeEvery:      "20",
		SettingKeyHealthRecoveryProbeInterval:   "300",
		SettingKeyHealthTimeoutRateThreshold:    "20",
		SettingKeyHealthSlowModelKeywords:       "thinking,opus,reasoning,long-context,long_context,200k,1m",
		SettingKeyCompactStrategyProbeEnabled:   "false",
	}
	for _, setting := range DefaultSettings() {
		if value, ok := want[setting.Key]; ok {
			if setting.Value != value {
				t.Fatalf("%s default value = %q, want %q", setting.Key, setting.Value, value)
			}
			delete(want, setting.Key)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing defaults: %+v", want)
	}
}

func TestSettingValidateHealthPolicyIntegers(t *testing.T) {
	keys := []SettingKey{
		SettingKeyHealthMinAdaptiveTimeout,
		SettingKeyHealthSlowModelMinTimeout,
		SettingKeyHealthRecoveryProbeEvery,
		SettingKeyHealthRecoveryProbeInterval,
		SettingKeyHealthTimeoutRateThreshold,
	}
	for _, key := range keys {
		valid := Setting{Key: key, Value: "10"}
		if err := valid.Validate(); err != nil {
			t.Fatalf("%s valid integer error = %v", key, err)
		}
		invalid := Setting{Key: key, Value: "bad"}
		if err := invalid.Validate(); err == nil {
			t.Fatalf("%s invalid integer error = nil", key)
		}
	}
}
