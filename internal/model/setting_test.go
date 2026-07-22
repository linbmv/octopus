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
		SettingKeyCircuitBreakerThreshold:       "2",
		SettingKeyRelayLogContentMode:           string(RelayLogContentModeMetadata),
		SettingKeyHealthWeightedBalancerEnabled: "false",
		SettingKeyHealthMinAdaptiveTimeout:      "15",
		SettingKeyHealthSlowModelMinTimeout:     "25",
		SettingKeyHealthRecoveryProbeEvery:      "20",
		SettingKeyHealthRecoveryProbeInterval:   "300",
		SettingKeyHealthTimeoutRateThreshold:    "20",
		SettingKeyHealthSlowModelKeywords:       "thinking,opus,reasoning,long-context,long_context,200k,1m",
		SettingKeyChannelCardPinnedIDs:          "[]",
		SettingKeyGroupCardPinnedIDs:            "[]",
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

func TestSettingValidateRelayLogContentMode(t *testing.T) {
	for _, value := range []string{
		string(RelayLogContentModeMetadata),
		string(RelayLogContentModeFull),
		string(RelayLogContentModeDisabled),
	} {
		setting := Setting{Key: SettingKeyRelayLogContentMode, Value: value}
		if err := setting.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", value, err)
		}
	}

	for _, value := range []string{"", "true", "FULL", "body"} {
		setting := Setting{Key: SettingKeyRelayLogContentMode, Value: value}
		if err := setting.Validate(); err == nil {
			t.Fatalf("Validate(%q) error = nil, want enum validation error", value)
		}
	}
}

func TestSettingValidateChannelCardPinnedIDs(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "empty", value: "[]"},
		{name: "multiple", value: "[21,2]"},
		{name: "duplicate", value: "[21,21]", wantErr: true},
		{name: "zero", value: "[0]", wantErr: true},
		{name: "negative", value: "[-1]", wantErr: true},
		{name: "not JSON", value: "21", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Setting{Key: SettingKeyChannelCardPinnedIDs, Value: tt.value}).Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestSettingValidateGroupCardPinnedIDs(t *testing.T) {
	for _, value := range []string{"[]", "[7,3]"} {
		if err := (&Setting{Key: SettingKeyGroupCardPinnedIDs, Value: value}).Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"[7,7]", "[0]", "not-json"} {
		if err := (&Setting{Key: SettingKeyGroupCardPinnedIDs, Value: value}).Validate(); err == nil {
			t.Fatalf("Validate(%q) error = nil, want error", value)
		}
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

func TestSettingValidateSchedulerIntervals(t *testing.T) {
	tests := []struct {
		name    string
		setting Setting
		wantErr bool
	}{
		{name: "stats integer", setting: Setting{Key: SettingKeyStatsSaveInterval, Value: "15"}},
		{name: "stats disabled", setting: Setting{Key: SettingKeyStatsSaveInterval, Value: "0"}},
		{name: "stats non integer", setting: Setting{Key: SettingKeyStatsSaveInterval, Value: "later"}, wantErr: true},
		{name: "stats negative", setting: Setting{Key: SettingKeyStatsSaveInterval, Value: "-1"}, wantErr: true},
		{name: "price negative", setting: Setting{Key: SettingKeyModelInfoUpdateInterval, Value: "-1"}, wantErr: true},
		{name: "sync negative", setting: Setting{Key: SettingKeySyncLLMInterval, Value: "-1"}, wantErr: true},
		{name: "unknown key", setting: Setting{Key: SettingKey("not_a_setting"), Value: "1"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.setting.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestSettingValidateRejectsNonFiniteMultiplier(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		setting := Setting{Key: SettingKeyHealthMaxMultiplierStack, Value: value}
		if err := setting.Validate(); err == nil {
			t.Fatalf("Validate(%q) error = nil, want error", value)
		}
	}
}

func TestSettingSchemasCoverAllDefaults(t *testing.T) {
	defaults := DefaultSettings()
	if len(defaults) != len(settingSchemas) {
		t.Fatalf("default count = %d, schema count = %d", len(defaults), len(settingSchemas))
	}
	for i := range defaults {
		if err := defaults[i].Validate(); err != nil {
			t.Fatalf("default %s=%q is invalid: %v", defaults[i].Key, defaults[i].Value, err)
		}
	}
}
