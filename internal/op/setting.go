package op

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

var settingCache = cache.New[model.SettingKey, string](16)
var settingsService = NewSettingsService(settingCache)

type SettingsService struct {
	cache cache.Cache[model.SettingKey, string]
}

func NewSettingsService(cacheStore cache.Cache[model.SettingKey, string]) *SettingsService {
	if cacheStore == nil {
		cacheStore = cache.New[model.SettingKey, string](16)
	}
	return &SettingsService{cache: cacheStore}
}

func DefaultSettingsService() *SettingsService {
	return settingsService
}

func SettingList(ctx context.Context) ([]model.Setting, error) {
	return settingsService.List(ctx)
}

func (s *SettingsService) List(ctx context.Context) ([]model.Setting, error) {
	settings := make([]model.Setting, 0, s.cache.Len())
	for key, value := range s.cache.GetAll() {
		settings = append(settings, model.Setting{
			Key:   key,
			Value: value,
		})
	}
	return settings, nil
}

func SettingGetString(key model.SettingKey) (string, error) {
	return settingsService.GetString(key)
}

func (s *SettingsService) GetString(key model.SettingKey) (string, error) {
	setting, ok := s.cache.Get(key)
	if !ok {
		return "", fmt.Errorf("setting not found")
	}
	return setting, nil
}

func SettingSetString(key model.SettingKey, value string) error {
	return settingsService.SetString(context.Background(), key, value)
}

func SettingSetStringContext(ctx context.Context, key model.SettingKey, value string) error {
	return settingsService.SetString(ctx, key, value)
}

func (s *SettingsService) SetString(ctx context.Context, key model.SettingKey, value string) error {
	valueCache, ok := s.cache.Get(key)
	if !ok {
		return fmt.Errorf("setting not found")
	}
	if valueCache == value {
		return nil
	}
	result := db.GetDB().WithContext(ctx).Model(&model.Setting{Key: key}).Update("Value", value)
	if result.Error != nil {
		return fmt.Errorf("failed to set setting: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("failed to set setting, key not found")
	}
	s.cache.Set(key, value)
	return nil
}

func SettingGetInt(key model.SettingKey) (int, error) {
	return settingsService.GetInt(key)
}

func (s *SettingsService) GetInt(key model.SettingKey) (int, error) {
	setting, ok := s.cache.Get(key)
	if !ok {
		return 0, fmt.Errorf("setting not found")
	}
	return strconv.Atoi(setting)
}

func SettingGetBool(key model.SettingKey) (bool, error) {
	return settingsService.GetBool(key)
}

func (s *SettingsService) GetBool(key model.SettingKey) (bool, error) {
	setting, ok := s.cache.Get(key)
	if !ok {
		return false, fmt.Errorf("setting not found")
	}
	return strconv.ParseBool(setting)
}

func SettingSetInt(key model.SettingKey, value int) error {
	return settingsService.SetInt(context.Background(), key, value)
}

func SettingSetIntContext(ctx context.Context, key model.SettingKey, value int) error {
	return settingsService.SetInt(ctx, key, value)
}

func (s *SettingsService) SetInt(ctx context.Context, key model.SettingKey, value int) error {
	valueCache, ok := s.cache.Get(key)
	if !ok {
		return fmt.Errorf("setting not found")
	}
	valueCacheNum, err := strconv.Atoi(valueCache)
	if err != nil {
		return fmt.Errorf("failed to set setting: %w", err)
	}
	if valueCacheNum == value {
		return nil
	}
	result := db.GetDB().WithContext(ctx).Model(&model.Setting{Key: key}).Update("Value", value)
	if result.Error != nil {
		return fmt.Errorf("failed to set setting: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("failed to set setting, key not found")
	}
	s.cache.Set(key, strconv.Itoa(value))
	return nil
}

func settingRefreshCache(ctx context.Context) error {
	return settingsService.RefreshCache(ctx)
}

func (s *SettingsService) RefreshCache(ctx context.Context) error {
	db := db.GetDB().WithContext(ctx)

	var settings []model.Setting
	if err := db.Find(&settings).Error; err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}

	existingKeys := make(map[model.SettingKey]bool)
	for _, setting := range settings {
		existingKeys[setting.Key] = true
	}

	defaultSettings := model.DefaultSettings()
	missingSettings := make([]model.Setting, 0, len(defaultSettings))

	for _, defaultSetting := range defaultSettings {
		if !existingKeys[defaultSetting.Key] {
			missingSettings = append(missingSettings, defaultSetting)
		}
	}

	if len(missingSettings) > 0 {
		if err := db.CreateInBatches(missingSettings, len(missingSettings)).Error; err != nil {
			return fmt.Errorf("failed to create missing settings: %w", err)
		}
		settings = append(settings, missingSettings...)
	}
	for _, setting := range settings {
		s.cache.Set(setting.Key, setting.Value)
	}
	return nil
}
