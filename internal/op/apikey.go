package op

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm"
)

var apiKeyCache = cache.New[int, model.APIKey](16)
var apiKeyIDMap = cache.New[string, int](16)

func APIKeyCreate(key *model.APIKey, ctx context.Context) error {
	if err := model.ValidateAPIKey(key); err != nil {
		return fmt.Errorf("%w: invalid API key: %v", ErrInvalidInput, err)
	}
	if err := validateAPIKeySupportedModels(key.SupportedModels, ctx); err != nil {
		return err
	}
	if key.APIKey == "" {
		return fmt.Errorf("%w: generated API key secret is empty", ErrInvalidInput)
	}
	if err := db.GetDB().WithContext(ctx).Create(key).Error; err != nil {
		return fmt.Errorf("failed to create API key: %w", err)
	}
	statsMu := statsLockFor(&statsService.apiKeyUpdateLocks, key.ID)
	statsMu.Lock()
	defer statsMu.Unlock()
	statsService.resetAPIKeyRequestStateLocked(key.ID)
	apiKeyCache.Set(key.ID, *key)
	apiKeyIDMap.Set(key.APIKey, key.ID)
	return nil
}

func APIKeyUpdate(key *model.APIKey, ctx context.Context) error {
	if key == nil || key.ID <= 0 {
		return fmt.Errorf("%w: API key id must be positive", ErrInvalidInput)
	}
	if err := model.ValidateAPIKey(key); err != nil {
		return fmt.Errorf("%w: invalid API key: %v", ErrInvalidInput, err)
	}
	if err := validateAPIKeySupportedModels(key.SupportedModels, ctx); err != nil {
		return err
	}
	statsMu := statsLockFor(&statsService.apiKeyUpdateLocks, key.ID)
	statsMu.Lock()
	defer statsMu.Unlock()

	existing, ok := apiKeyCache.Get(key.ID)
	if !ok {
		return fmt.Errorf("%w: API key not found", ErrNotFound)
	}
	result := db.GetDB().WithContext(ctx).Model(&model.APIKey{}).Where("id = ?", key.ID).Updates(map[string]any{
		"name":             key.Name,
		"enabled":          key.Enabled,
		"expire_at":        key.ExpireAt,
		"max_cost":         key.MaxCost,
		"supported_models": key.SupportedModels,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update API key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := db.GetDB().WithContext(ctx).Model(&model.APIKey{}).Where("id = ?", key.ID).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify API key update: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("%w: API key not found", ErrNotFound)
		}
	}
	key.APIKey = existing.APIKey
	apiKeyCache.Set(key.ID, *key)
	return nil
}

func validateAPIKeySupportedModels(value string, ctx context.Context) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	groups, err := GroupList(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot validate API key supported models: %v", ErrInvalidInput, err)
	}
	allowed := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		allowed[group.Name] = struct{}{}
	}
	for _, part := range strings.Split(value, ",") {
		name := strings.TrimSpace(part)
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%w: API key supported model %q does not reference an existing group", ErrInvalidInput, name)
		}
	}
	return nil
}

func APIKeyList(ctx context.Context) ([]model.APIKey, error) {
	keys := make([]model.APIKey, 0, apiKeyCache.Len())
	for _, apiKey := range apiKeyCache.GetAll() {
		keys = append(keys, apiKey)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	return keys, nil
}

func APIKeyGet(id int, ctx context.Context) (model.APIKey, error) {
	apiKey, ok := apiKeyCache.Get(id)
	if !ok {
		return model.APIKey{}, fmt.Errorf("%w: API key not found", ErrNotFound)
	}
	return apiKey, nil
}

func APIKeyGetByAPIKey(apiKey string, ctx context.Context) (model.APIKey, error) {
	id, ok := apiKeyIDMap.Get(apiKey)
	if !ok {
		return model.APIKey{}, fmt.Errorf("%w: API key not found", ErrNotFound)
	}
	return APIKeyGet(id, ctx)
}

func APIKeyDelete(id int, ctx context.Context) error {
	statsMu := statsLockFor(&statsService.apiKeyUpdateLocks, id)
	statsMu.Lock()
	defer statsMu.Unlock()

	var key model.APIKey
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.First(&key, id)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: API key not found", ErrNotFound)
			}
			return fmt.Errorf("failed to get API key: %w", result.Error)
		}

		if result := tx.Where("api_key_id = ?", id).Delete(&model.StatsAPIKey{}); result.Error != nil {
			return fmt.Errorf("failed to delete stats API key: %w", result.Error)
		}

		result = tx.Delete(&key)
		if result.Error != nil {
			return fmt.Errorf("failed to delete API key: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: API key not found", ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return err
	}

	apiKeyCache.Del(key.ID)
	apiKeyIDMap.Del(key.APIKey)
	statsService.apiKeys.Del(id)
	statsService.dirtyAPIKeys.delete(id)
	statsService.markAPIKeyDeletedLocked(id)
	return nil
}

func apiKeyRefreshCache(ctx context.Context) error {
	apiKeys := []model.APIKey{}
	if err := db.GetDB().WithContext(ctx).Find(&apiKeys).Error; err != nil {
		return err
	}
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	for _, apiKey := range apiKeys {
		apiKeyCache.Set(apiKey.ID, apiKey)
		apiKeyIDMap.Set(apiKey.APIKey, apiKey.ID)
	}
	return nil
}
