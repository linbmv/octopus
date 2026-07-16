package op

import (
	"context"
	"fmt"
	"sort"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

var llmModelCache = cache.New[string, model.LLMPrice](16)

func LLMList(ctx context.Context) ([]model.LLMInfo, error) {
	models := make([]model.LLMInfo, 0, llmModelCache.Len())
	for m, cost := range llmModelCache.GetAll() {
		models = append(models, model.LLMInfo{
			Name:     m,
			LLMPrice: cost,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

func LLMUpdate(info model.LLMInfo, ctx context.Context) error {
	if err := model.ValidateLLMInfo(&info); err != nil {
		return fmt.Errorf("%w: invalid model: %v", ErrInvalidInput, err)
	}
	_, ok := llmModelCache.Get(info.Name)
	if !ok {
		return fmt.Errorf("%w: model not found", ErrNotFound)
	}
	result := db.GetDB().WithContext(ctx).Model(&model.LLMInfo{}).Where("name = ?", info.Name).Updates(map[string]any{
		"input":       info.Input,
		"output":      info.Output,
		"cache_read":  info.CacheRead,
		"cache_write": info.CacheWrite,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := db.GetDB().WithContext(ctx).Model(&model.LLMInfo{}).Where("name = ?", info.Name).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify model update: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("%w: model not found", ErrNotFound)
		}
	}
	llmModelCache.Set(info.Name, info.LLMPrice)
	return nil
}

func LLMDelete(modelName string, ctx context.Context) error {
	probe := model.LLMInfo{Name: modelName}
	if err := model.ValidateLLMInfo(&probe); err != nil {
		return fmt.Errorf("%w: invalid model name: %v", ErrInvalidInput, err)
	}
	modelName = probe.Name
	_, ok := llmModelCache.Get(modelName)
	if !ok {
		return fmt.Errorf("%w: model not found", ErrNotFound)
	}
	result := db.GetDB().WithContext(ctx).Delete(&model.LLMInfo{Name: modelName})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: model not found", ErrNotFound)
	}
	llmModelCache.Del(modelName)
	return nil
}
func LLMBatchDelete(modelNames []string, ctx context.Context) error {
	if len(modelNames) == 0 {
		return nil
	}
	for i := range modelNames {
		probe := model.LLMInfo{Name: modelNames[i]}
		if err := model.ValidateLLMInfo(&probe); err != nil {
			return fmt.Errorf("%w: invalid model name: %v", ErrInvalidInput, err)
		}
		modelNames[i] = probe.Name
	}
	if err := db.GetDB().WithContext(ctx).Where("name IN ?", modelNames).Delete(&model.LLMInfo{}).Error; err != nil {
		return err
	}
	llmModelCache.Del(modelNames...)
	return nil
}
func LLMCreate(info model.LLMInfo, ctx context.Context) error {
	if err := model.ValidateLLMInfo(&info); err != nil {
		return fmt.Errorf("%w: invalid model: %v", ErrInvalidInput, err)
	}
	_, ok := llmModelCache.Get(info.Name)
	if ok {
		return fmt.Errorf("%w: model already exists", ErrConflict)
	}
	if err := db.GetDB().WithContext(ctx).Create(&info).Error; err != nil {
		return err
	}
	llmModelCache.Set(info.Name, info.LLMPrice)
	return nil
}
func LLMBatchCreate(llmInfos []model.LLMInfo, ctx context.Context) error {
	if len(llmInfos) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(llmInfos))
	newLLMInfos := make([]model.LLMInfo, 0, len(llmInfos))
	for i, llmInfo := range llmInfos {
		if err := model.ValidateLLMInfo(&llmInfo); err != nil {
			return fmt.Errorf("%w: invalid model at index %d: %v", ErrInvalidInput, i, err)
		}
		if _, ok := seen[llmInfo.Name]; ok {
			continue
		}
		if _, ok := llmModelCache.Get(llmInfo.Name); ok {
			continue
		}
		seen[llmInfo.Name] = struct{}{}
		newLLMInfos = append(newLLMInfos, llmInfo)
	}
	if len(newLLMInfos) == 0 {
		return nil
	}
	if err := db.GetDB().WithContext(ctx).Create(&newLLMInfos).Error; err != nil {
		return err
	}
	for _, llmInfo := range newLLMInfos {
		llmModelCache.Set(llmInfo.Name, llmInfo.LLMPrice)
	}
	return nil
}
func LLMGet(name string) (model.LLMPrice, error) {
	price, ok := llmModelCache.Get(name)
	if !ok {
		return model.LLMPrice{}, fmt.Errorf("%w: model not found", ErrNotFound)
	}
	return price, nil
}

func llmRefreshCache(ctx context.Context) error {
	models := []model.LLMInfo{}
	if err := db.GetDB().WithContext(ctx).Find(&models).Error; err != nil {
		return err
	}
	llmModelCache.Clear()
	for _, model := range models {
		llmModelCache.Set(model.Name, model.LLMPrice)
	}
	return nil
}
