package task

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/utils/log"
)

const (
	TaskPriceUpdate  = "price_update"
	TaskStatsSave    = "stats_save"
	TaskRelayLogSave = "relay_log_save"
	TaskSyncLLM      = "sync_llm"
	TaskCleanLLM     = "clean_llm"
	TaskBaseUrlDelay = "base_url_delay"
)

type settingIntGetter func(model.SettingKey) (int, error)

func Init() {
	initWithSettingGetter(op.SettingGetInt)
}

func initWithSettingGetter(getInt settingIntGetter) {
	// Every task definition is registered even when one persisted setting is bad.
	// A bad value falls back independently, so it cannot leave the scheduler only
	// partially initialized and a later setting update can still reconfigure it.
	priceUpdateInterval := configuredInterval(getInt, model.SettingKeyModelInfoUpdateInterval, time.Hour)
	RegisterContext(string(model.SettingKeyModelInfoUpdateInterval), priceUpdateInterval, true, func(ctx context.Context) error {
		if err := price.UpdateLLMPrice(ctx); err != nil {
			return fmt.Errorf("update price info: %w", err)
		}
		return nil
	})

	RegisterContext(TaskBaseUrlDelay, 1*time.Hour, true, ChannelBaseUrlDelayTaskContext)

	syncLLMInterval := configuredInterval(getInt, model.SettingKeySyncLLMInterval, time.Hour)
	RegisterContext(string(model.SettingKeySyncLLMInterval), syncLLMInterval, true, SyncModelsTaskContext)

	statsSaveInterval := configuredInterval(getInt, model.SettingKeyStatsSaveInterval, time.Minute)
	RegisterContext(TaskStatsSave, statsSaveInterval, false, op.StatsSaveDBTaskContext)

	RegisterContext(TaskRelayLogSave, 10*time.Minute, false, func(ctx context.Context) error {
		if err := op.RelayLogSaveDBTask(ctx); err != nil {
			return fmt.Errorf("relay log save db task: %w", err)
		}
		return nil
	})
}

func configuredInterval(getInt settingIntGetter, key model.SettingKey, unit time.Duration) time.Duration {
	value, err := getInt(key)
	maxValue := int64((time.Duration(1<<63 - 1)) / unit)
	if err != nil || value < 0 || int64(value) > maxValue {
		fallback, fallbackErr := defaultSettingInt(key)
		if fallbackErr != nil {
			log.Errorf("failed to configure task %s: %v", key, fallbackErr)
			return 0
		}
		if err != nil {
			log.Warnf("failed to get %s; using default %d: %v", key, fallback, err)
		} else {
			log.Warnf("invalid %s value %d; using default %d", key, value, fallback)
		}
		value = fallback
	}
	return time.Duration(value) * unit
}

func defaultSettingInt(key model.SettingKey) (int, error) {
	for _, setting := range model.DefaultSettings() {
		if setting.Key == key {
			value, err := strconv.Atoi(setting.Value)
			if err != nil {
				return 0, fmt.Errorf("invalid default for %s: %w", key, err)
			}
			return value, nil
		}
	}
	return 0, fmt.Errorf("default for %s not found", key)
}

// ReconfigureSetting applies scheduler-backed settings after their database
// update succeeds. Other settings have no scheduler side effect.
func ReconfigureSetting(key model.SettingKey, value string) error {
	var name string
	var unit time.Duration
	switch key {
	case model.SettingKeyModelInfoUpdateInterval:
		name = string(key)
		unit = time.Hour
	case model.SettingKeySyncLLMInterval:
		name = string(key)
		unit = time.Hour
	case model.SettingKeyStatsSaveInterval:
		name = TaskStatsSave
		unit = time.Minute
	default:
		return nil
	}

	setting := model.Setting{Key: key, Value: value}
	if err := setting.Validate(); err != nil {
		return err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	if err := Update(name, time.Duration(parsed)*unit); err != nil {
		return fmt.Errorf("reconfigure %s: %w", key, err)
	}
	return nil
}
