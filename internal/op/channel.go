package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"github.com/charmbracelet/log"
	"github.com/looplj/axonhub/llm/httpclient"
	"gorm.io/gorm"
)

var (
	channelCache      = cache.New[int, model.Channel](16)      // 渠道配置的进程内副本。
	channelModelCache = cache.New[int, model.ChannelModel](16) // 渠道模型及其统计的进程内副本。
)

// ChannelList 返回缓存中的全部渠道及其模型。
func ChannelList() []model.Channel {
	channels := make([]model.Channel, 0, channelCache.Len())
	for _, channel := range channelCache.GetAll() {
		channels = append(channels, channelSnapshot(channel))
	}
	return channels
}

// ChannelCreate 创建渠道及其模型并写入缓存。
func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	if channel == nil {
		return fmt.Errorf("channel is required")
	}
	normalizeChannelCompatibility(channel)
	if err := validateHeaderRules(channel.HeaderRules); err != nil {
		return err
	}
	if err := validateJSONRewriteRules(channel.JSONRewriteRules); err != nil {
		return err
	}
	channel.ID = 0
	channel.StatsMetrics = model.StatsMetrics{}
	for i := range channel.Keys {
		channel.Keys[i].ID = 0
		channel.Keys[i].ChannelID = 0
		if strings.TrimSpace(channel.Keys[i].ChannelKey) == "" {
			return fmt.Errorf("channel key is required")
		}
	}
	for i := range channel.Models {
		channel.Models[i].ID = 0
		channel.Models[i].ChannelID = 0
		channel.Models[i].StatsMetrics = model.StatsMetrics{}
		channel.Models[i].Name = strings.TrimSpace(channel.Models[i].Name)
		if channel.Models[i].Source == "" {
			channel.Models[i].Source = model.ChannelModelSourceManual
		}
		if channel.Models[i].Name == "" {
			return fmt.Errorf("channel model name is required")
		}
	}
	if err := db.GetDB().WithContext(ctx).Create(channel).Error; err != nil {
		return err
	}
	cachedChannel := *channel
	cachedChannel.Models = nil
	channelCache.Set(channel.ID, cachedChannel)
	for _, channelModel := range channel.Models {
		channelModelCache.Set(channelModel.ID, channelModel)
	}
	return nil
}

// ChannelUpdate 更新渠道配置和模型行，并删除不再提供的模型。
func ChannelUpdate(req *model.ChannelUpdateRequest, ctx context.Context) (*model.Channel, error) {
	if _, ok := channelCache.Get(req.ID); !ok {
		return nil, fmt.Errorf("channel not found")
	}

	var selectFields []string
	updates := model.Channel{ID: req.ID}
	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Type != nil {
		selectFields = append(selectFields, "type")
		updates.Type = *req.Type
	}
	if req.Enabled != nil {
		selectFields = append(selectFields, "enabled")
		updates.Enabled = *req.Enabled
	}
	if req.BaseURL != nil {
		selectFields = append(selectFields, "base_url")
		updates.BaseURL = *req.BaseURL
		if req.BaseUrls == nil {
			selectFields = append(selectFields, "base_urls")
			if strings.TrimSpace(*req.BaseURL) == "" {
				updates.BaseUrls = []model.BaseUrl{}
			} else {
				updates.BaseUrls = []model.BaseUrl{{URL: *req.BaseURL}}
			}
		}
	}
	if req.BaseUrls != nil {
		baseURLs := append([]model.BaseUrl(nil), (*req.BaseUrls)...)
		selectFields = append(selectFields, "base_urls")
		updates.BaseUrls = baseURLs
		if req.BaseURL == nil {
			selectFields = append(selectFields, "base_url")
			if len(baseURLs) > 0 {
				updates.BaseURL = baseURLs[0].URL
			} else {
				updates.BaseURL = ""
			}
		}
	}
	if req.Key != nil {
		selectFields = append(selectFields, "key")
		updates.Key = *req.Key
	}
	if req.Proxy != nil {
		selectFields = append(selectFields, "proxy")
		updates.Proxy = *req.Proxy
	}
	if req.AutoSync != nil {
		selectFields = append(selectFields, "auto_sync")
		updates.AutoSync = *req.AutoSync
	}
	if req.CustomHeader != nil {
		selectFields = append(selectFields, "custom_header")
		updates.CustomHeader = *req.CustomHeader
	}
	if req.HeaderRules != nil {
		if err := validateHeaderRules(*req.HeaderRules); err != nil {
			return nil, err
		}
		selectFields = append(selectFields, "header_rules")
		updates.HeaderRules = *req.HeaderRules
	}
	if req.JSONRewriteRules != nil {
		if err := validateJSONRewriteRules(*req.JSONRewriteRules); err != nil {
			return nil, err
		}
		selectFields = append(selectFields, "json_rewrite_rules")
		updates.JSONRewriteRules = *req.JSONRewriteRules
	}
	if req.ChannelProxy != nil {
		selectFields = append(selectFields, "channel_proxy")
		updates.ChannelProxy = req.ChannelProxy
	}
	if req.ParamOverride != nil {
		selectFields = append(selectFields, "param_override")
		updates.ParamOverride = req.ParamOverride
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = req.MatchRegex
	}

	var currentModels []model.ChannelModel
	var channel model.Channel
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(selectFields) > 0 {
			if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
				return fmt.Errorf("failed to update channel: %w", err)
			}
		}
		if req.Models != nil {
			if err := syncChannelModels(tx, req.ID, *req.Models); err != nil {
				return err
			}
		}
		if req.Key != nil {
			if err := syncPrimaryChannelKey(tx, req.ID, *req.Key); err != nil {
				return err
			}
		}
		if req.BaseUrls != nil {
			if err := validateBaseURLs(*req.BaseUrls); err != nil {
				return err
			}
		}
		if err := syncChannelKeys(tx, req.ID, req.KeysToAdd, req.KeysToUpdate, req.KeysToDelete); err != nil {
			return err
		}
		if req.Key == nil && (len(req.KeysToAdd) > 0 || len(req.KeysToUpdate) > 0 || len(req.KeysToDelete) > 0) {
			if err := syncChannelKeyAlias(tx, req.ID); err != nil {
				return err
			}
		}
		if req.Models != nil {
			if err := tx.Where("channel_id = ?", req.ID).Find(&currentModels).Error; err != nil {
				return fmt.Errorf("failed to load channel models: %w", err)
			}
		}
		if err := tx.Preload("Keys").First(&channel, req.ID).Error; err != nil {
			return fmt.Errorf("failed to load updated channel: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	channelStatsNeedUpdateLock.Lock()
	if cachedChannel, ok := channelCache.Get(channel.ID); ok {
		channel.StatsMetrics = cachedChannel.StatsMetrics
	}
	cachedChannel := channel
	cachedChannel.Models = nil
	channelCache.Set(channel.ID, cachedChannel)
	channelStatsNeedUpdateLock.Unlock()
	if req.Models != nil {
		currentModelsByID := make(map[int]model.ChannelModel, len(currentModels))
		for _, currentModel := range currentModels {
			currentModelsByID[currentModel.ID] = currentModel
		}

		// 仅增删发生变化的缓存项，存活模型保留尚未落库的统计。
		channelModelStatsNeedUpdateLock.Lock()
		for _, cachedModel := range channelModelCache.GetAll() {
			if cachedModel.ChannelID != req.ID {
				continue
			}
			currentModel, exists := currentModelsByID[cachedModel.ID]
			if !exists {
				channelModelCache.Del(cachedModel.ID)
				delete(channelModelStatsNeedUpdate, cachedModel.ID)
				continue
			}
			cachedModel.Source = currentModel.Source
			channelModelCache.Set(cachedModel.ID, cachedModel)
			delete(currentModelsByID, cachedModel.ID)
		}
		for _, addedModel := range currentModelsByID {
			channelModelCache.Set(addedModel.ID, addedModel)
		}
		channelModelStatsNeedUpdateLock.Unlock()

		if err := groupRefreshCache(ctx); err != nil {
			return nil, fmt.Errorf("failed to refresh groups: %w", err)
		}
	}
	snapshot := channelSnapshot(channel)
	return &snapshot, nil
}

// ChannelEnabled 更新渠道启用状态。
func ChannelEnabled(id int, enabled bool, ctx context.Context) error {
	if _, ok := channelCache.Get(id); !ok {
		return fmt.Errorf("channel not found")
	}
	if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return err
	}
	channelStatsNeedUpdateLock.Lock()
	if channel, ok := channelCache.Get(id); ok {
		channel.Enabled = enabled
		channelCache.Set(id, channel)
	}
	channelStatsNeedUpdateLock.Unlock()
	return nil
}

// ChannelDel 删除渠道及其模型，关联分组成员由数据库外键级联删除。
func ChannelDel(id int, ctx context.Context) error {
	if _, ok := channelCache.Get(id); !ok {
		return fmt.Errorf("channel not found")
	}
	var modelIDs []int
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ChannelModel{}).Where("channel_id = ?", id).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("failed to find channel models: %w", err)
		}
		if len(modelIDs) > 0 {
			if err := clearActiveItemsByChannelModels(tx, modelIDs); err != nil {
				return err
			}
		}
		if err := tx.Delete(&model.Channel{}, id).Error; err != nil {
			return fmt.Errorf("failed to delete channel: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	channelStatsNeedUpdateLock.Lock()
	channelCache.Del(id)
	delete(channelStatsNeedUpdate, id)
	channelStatsNeedUpdateLock.Unlock()
	channelModelStatsNeedUpdateLock.Lock()
	channelModelCache.Del(modelIDs...)
	for _, modelID := range modelIDs {
		delete(channelModelStatsNeedUpdate, modelID)
	}
	channelModelStatsNeedUpdateLock.Unlock()
	if err := groupRefreshCache(ctx); err != nil {
		return fmt.Errorf("failed to refresh groups: %w", err)
	}
	return nil
}

// ChannelGet 返回指定渠道的缓存副本及其模型。
func ChannelGet(id int) (model.Channel, error) {
	channel, ok := channelCache.Get(id)
	if !ok {
		return model.Channel{}, fmt.Errorf("channel not found")
	}
	return channelSnapshot(channel), nil
}

// ChannelModelGet 返回指定渠道模型的缓存副本。
func ChannelModelGet(id int) (model.ChannelModel, error) {
	channelModel, ok := channelModelCache.Get(id)
	if !ok {
		return model.ChannelModel{}, fmt.Errorf("channel model not found")
	}
	return channelModel, nil
}

// channelRefreshCache 从数据库刷新渠道和渠道模型缓存。
func channelRefreshCache(ctx context.Context) error {
	channels := []model.Channel{}
	if err := db.GetDB().WithContext(ctx).Preload("Keys").Find(&channels).Error; err != nil {
		log.Warnf("failed to get channels: %v", err)
		return err
	}
	channelModels := []model.ChannelModel{}
	if err := db.GetDB().WithContext(ctx).Find(&channelModels).Error; err != nil {
		return err
	}
	channelCache.Clear()
	channelModelCache.Clear()
	for _, channel := range channels {
		channel.Models = nil
		channelCache.Set(channel.ID, channel)
	}
	for _, channelModel := range channelModels {
		channelModelCache.Set(channelModel.ID, channelModel)
	}
	return nil
}

func normalizeChannelCompatibility(channel *model.Channel) {
	if channel == nil {
		return
	}
	if len(channel.BaseUrls) == 0 && strings.TrimSpace(channel.BaseURL) != "" {
		channel.BaseUrls = []model.BaseUrl{{URL: channel.BaseURL}}
	}
	if strings.TrimSpace(channel.BaseURL) == "" && len(channel.BaseUrls) > 0 {
		channel.BaseURL = channel.BaseUrls[0].URL
	}
	if len(channel.Keys) == 0 && strings.TrimSpace(channel.Key) != "" {
		channel.Keys = []model.ChannelKey{{Enabled: true, ChannelKey: channel.Key}}
	}
	if strings.TrimSpace(channel.Key) == "" && len(channel.Keys) > 0 {
		for _, key := range channel.Keys {
			if strings.TrimSpace(key.ChannelKey) != "" {
				channel.Key = key.ChannelKey
				break
			}
		}
	}
}

func validateBaseURLs(urls []model.BaseUrl) error {
	for _, baseURL := range urls {
		if strings.TrimSpace(baseURL.URL) == "" {
			return fmt.Errorf("channel base URL is required")
		}
		if baseURL.Delay < 0 {
			return fmt.Errorf("channel base URL delay cannot be negative")
		}
	}
	return nil
}

// validateHeaderRules 在写库前拒绝非法规则, 避免坏配置留到转发时才失败。
// 凭据类 Header 由 relay 在应用时静默跳过, 这里提前报错让管理员看到原因。
func validateHeaderRules(rules []model.HeaderRule) error {
	for i, rule := range rules {
		if strings.TrimSpace(rule.HeaderKey) == "" {
			return fmt.Errorf("header rule %d: header key is required", i)
		}
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "set", "append", "remove":
		default:
			return fmt.Errorf("header rule %d: action must be set, append or remove", i)
		}
		if requestrewrite.IsProtectedHeader(rule.HeaderKey) || httpclient.IsSensitiveHeader(rule.HeaderKey) {
			return fmt.Errorf("header rule %d: %s carries upstream credentials and cannot be rewritten", i, rule.HeaderKey)
		}
	}
	return nil
}

// validateJSONRewriteRules 校验 Pointer 语法与 override 取值, 保证转发时不会因
// 配置错误中断请求。
func validateJSONRewriteRules(rules []model.JSONRewriteRule) error {
	for i, rule := range rules {
		if _, err := requestrewrite.ParseJSONPointer(strings.TrimSpace(rule.Path)); err != nil {
			return fmt.Errorf("json rewrite rule %d: %w", i, err)
		}
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "remove":
		case "override":
			if rule.Value == nil {
				return fmt.Errorf("json rewrite rule %d: override requires a value", i)
			}
			if !json.Valid([]byte(*rule.Value)) {
				return fmt.Errorf("json rewrite rule %d: value must be valid JSON", i)
			}
		default:
			return fmt.Errorf("json rewrite rule %d: action must be override or remove", i)
		}
	}
	return nil
}

func syncPrimaryChannelKey(tx *gorm.DB, channelID int, value string) error {
	value = strings.TrimSpace(value)
	var key model.ChannelKey
	err := tx.Where("channel_id = ?", channelID).Order("id ASC").First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if value == "" {
			return nil
		}
		return tx.Create(&model.ChannelKey{ChannelID: channelID, Enabled: true, ChannelKey: value}).Error
	}
	if err != nil {
		return fmt.Errorf("failed to load primary channel key: %w", err)
	}
	if err := tx.Model(&model.ChannelKey{}).Where("id = ?", key.ID).Update("channel_key", value).Error; err != nil {
		return fmt.Errorf("failed to update primary channel key: %w", err)
	}
	return nil
}

func syncChannelKeyAlias(tx *gorm.DB, channelID int) error {
	var key model.ChannelKey
	err := tx.Where("channel_id = ?", channelID).Order("id ASC").First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Model(&model.Channel{}).Where("id = ?", channelID).Update("key", "").Error
	}
	if err != nil {
		return fmt.Errorf("failed to load channel key alias: %w", err)
	}
	return tx.Model(&model.Channel{}).Where("id = ?", channelID).Update("key", key.ChannelKey).Error
}

func syncChannelKeys(tx *gorm.DB, channelID int, adds []model.ChannelKeyAddRequest, updates []model.ChannelKeyUpdateRequest, deletes []int) error {
	for _, add := range adds {
		value := strings.TrimSpace(add.ChannelKey)
		if value == "" {
			return fmt.Errorf("channel key is required")
		}
		if err := tx.Create(&model.ChannelKey{ChannelID: channelID, Enabled: add.Enabled, ChannelKey: value, Remark: add.Remark}).Error; err != nil {
			return fmt.Errorf("failed to add channel key: %w", err)
		}
	}
	for _, update := range updates {
		var fields map[string]any
		if update.Enabled != nil {
			fields = map[string]any{"enabled": *update.Enabled}
		}
		if update.ChannelKey != nil {
			value := strings.TrimSpace(*update.ChannelKey)
			if value == "" {
				return fmt.Errorf("channel key is required")
			}
			if fields == nil {
				fields = map[string]any{}
			}
			fields["channel_key"] = value
		}
		if update.Remark != nil {
			if fields == nil {
				fields = map[string]any{}
			}
			fields["remark"] = *update.Remark
		}
		if len(fields) == 0 {
			continue
		}
		result := tx.Model(&model.ChannelKey{}).Where("id = ? AND channel_id = ?", update.ID, channelID).Updates(fields)
		if result.Error != nil {
			return fmt.Errorf("failed to update channel key: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("channel key not found: %d", update.ID)
		}
	}
	if len(deletes) > 0 {
		result := tx.Where("id IN ? AND channel_id = ?", deletes, channelID).Delete(&model.ChannelKey{})
		if result.Error != nil {
			return fmt.Errorf("failed to delete channel keys: %w", result.Error)
		}
		if result.RowsAffected != int64(len(deletes)) {
			return fmt.Errorf("one or more channel keys not found")
		}
	}
	return nil
}

// channelSnapshot 将渠道缓存与当前渠道模型合并为读取副本。
func channelSnapshot(channel model.Channel) model.Channel {
	models := make([]model.ChannelModel, 0)
	for _, channelModel := range channelModelCache.GetAll() {
		if channelModel.ChannelID == channel.ID {
			models = append(models, channelModel)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	channel.Models = models
	return channel
}

// syncChannelModels 按提交的模型集合新增、删除渠道模型，并更新变化的来源。
func syncChannelModels(tx *gorm.DB, channelID int, requested []model.ChannelModel) error {
	var existing []model.ChannelModel
	if err := tx.Where("channel_id = ?", channelID).Find(&existing).Error; err != nil {
		return fmt.Errorf("failed to load channel models: %w", err)
	}
	existingByName := make(map[string]model.ChannelModel, len(existing))
	for _, channelModel := range existing {
		existingByName[channelModel.Name] = channelModel
	}
	for _, requestedModel := range requested {
		name := strings.TrimSpace(requestedModel.Name)
		if name == "" {
			return fmt.Errorf("channel model name is required")
		}
		source := requestedModel.Source
		if source == "" {
			source = model.ChannelModelSourceManual
		}
		if current, ok := existingByName[name]; ok {
			if current.Source != source {
				if err := tx.Model(&model.ChannelModel{}).Where("id = ?", current.ID).Update("source", source).Error; err != nil {
					return fmt.Errorf("failed to update channel model: %w", err)
				}
			}
			delete(existingByName, name)
			continue
		}
		if err := tx.Create(&model.ChannelModel{ChannelID: channelID, Name: name, Source: source}).Error; err != nil {
			return fmt.Errorf("failed to create channel model: %w", err)
		}
	}
	deletedModelIDs := make([]int, 0, len(existingByName))
	for _, channelModel := range existingByName {
		deletedModelIDs = append(deletedModelIDs, channelModel.ID)
	}
	if len(deletedModelIDs) == 0 {
		return nil
	}
	if err := clearActiveItemsByChannelModels(tx, deletedModelIDs); err != nil {
		return err
	}
	if err := tx.Delete(&model.ChannelModel{}, deletedModelIDs).Error; err != nil {
		return fmt.Errorf("failed to delete channel models: %w", err)
	}
	return nil
}

// clearActiveItemsByChannelModels 清理引用待删除渠道模型的分组当前项。
func clearActiveItemsByChannelModels(tx *gorm.DB, channelModelIDs []int) error {
	if len(channelModelIDs) == 0 {
		return nil
	}
	itemIDs := tx.Model(&model.GroupItem{}).
		Select("id").Where("channel_model_id IN ?", channelModelIDs)
	if err := tx.Model(&model.Group{}).
		Where("active_item_id IN (?)", itemIDs).
		Update("active_item_id", 0).Error; err != nil {
		return fmt.Errorf("failed to clear active items: %w", err)
	}
	return nil
}
