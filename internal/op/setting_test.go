package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

func TestSettingsServiceReadsFromCache(t *testing.T) {
	store := cache.New[model.SettingKey, string](1)
	store.Set(model.SettingKeySmartHealthEnabled, "true")
	store.Set(model.SettingKeyHealthRecoveryProbeEvery, "25")
	service := NewSettingsService(store)

	enabled, err := service.GetBool(model.SettingKeySmartHealthEnabled)
	if err != nil {
		t.Fatalf("GetBool returned error: %v", err)
	}
	if !enabled {
		t.Fatal("GetBool = false, want true")
	}

	probeEvery, err := service.GetInt(model.SettingKeyHealthRecoveryProbeEvery)
	if err != nil {
		t.Fatalf("GetInt returned error: %v", err)
	}
	if probeEvery != 25 {
		t.Fatalf("GetInt = %d, want 25", probeEvery)
	}

	settings, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(settings) != 2 {
		t.Fatalf("List length = %d, want 2", len(settings))
	}
}

func TestSettingsServiceMissingSetting(t *testing.T) {
	service := NewSettingsService(cache.New[model.SettingKey, string](1))
	if _, err := service.GetString(model.SettingKeySmartHealthEnabled); err == nil {
		t.Fatal("GetString missing key returned nil error")
	}
}

func TestSettingsServiceRejectsInvalidValueBeforePersistence(t *testing.T) {
	store := cache.New[model.SettingKey, string](1)
	store.Set(model.SettingKeyStatsSaveInterval, "10")
	service := NewSettingsService(store)

	if err := service.SetString(context.Background(), model.SettingKeyStatsSaveInterval, "not-an-integer"); err == nil {
		t.Fatal("SetString invalid interval returned nil error")
	}
	value, ok := store.Get(model.SettingKeyStatsSaveInterval)
	if !ok || value != "10" {
		t.Fatalf("cached value = %q, %v; want unchanged value 10", value, ok)
	}
}
