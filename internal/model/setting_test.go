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
	for _, setting := range DefaultSettings() {
		if setting.Key == SettingKeyHealthWeightedBalancerEnabled {
			if setting.Value != "true" {
				t.Fatalf("default value = %q, want true", setting.Value)
			}
			return
		}
	}
	t.Fatal("health weighted balancer setting missing from defaults")
}
