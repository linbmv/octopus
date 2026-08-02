package op

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
	"gorm.io/gorm"
)

var channelCache = cache.New[int, model.Channel](16)
var channelKeyCache = cache.New[int, model.ChannelKey](16)
var channelKeyCacheNeedUpdate = newDirtySet()
var channelService = NewChannelService(channelCache, channelKeyCache)
var channelKeyPersistenceMu sync.Mutex

type ChannelService struct {
	channels    cache.Cache[int, model.Channel]
	channelKeys cache.Cache[int, model.ChannelKey]
}

func NewChannelService(channels cache.Cache[int, model.Channel], channelKeys cache.Cache[int, model.ChannelKey]) *ChannelService {
	if channels == nil {
		channels = cache.New[int, model.Channel](16)
	}
	if channelKeys == nil {
		channelKeys = cache.New[int, model.ChannelKey](16)
	}
	return &ChannelService{
		channels:    channels,
		channelKeys: channelKeys,
	}
}

func ChannelList(ctx context.Context) ([]model.Channel, error) {
	return channelService.List(ctx)
}

func (s *ChannelService) List(ctx context.Context) ([]model.Channel, error) {
	channels := make([]model.Channel, 0, s.channels.Len())
	for _, channel := range s.channels.GetAll() {
		channels = append(channels, channel)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })
	return channels, nil
}

func ChannelCreate(channel *model.Channel, ctx context.Context) error {
	if err := model.ValidateChannel(channel); err != nil {
		return fmt.Errorf("%w: invalid channel: %v", ErrInvalidInput, err)
	}
	for _, existing := range channelCache.GetAll() {
		if existing.Name == channel.Name {
			return fmt.Errorf("%w: channel name already exists", ErrConflict)
		}
	}
	if err := db.GetDB().WithContext(ctx).Create(channel).Error; err != nil {
		return err
	}
	channelCache.Set(channel.ID, *channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}

// ChannelKeyUpdate 仅更新 ChannelKey 的内存缓存（不落库），并标记为需要在 SaveCache 时写入数据库。
func ChannelKeyUpdate(key model.ChannelKey) error {
	if key.ID == 0 || key.ChannelID == 0 {
		return fmt.Errorf("invalid channel key")
	}
	ch, ok := channelCache.Get(key.ChannelID)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	if len(ch.Keys) > 0 {
		keys := make([]model.ChannelKey, len(ch.Keys))
		copy(keys, ch.Keys)
		for i := range keys {
			if keys[i].ID == key.ID {
				keys[i] = key
				break
			}
		}
		ch.Keys = keys
	}
	channelCache.Set(key.ChannelID, ch)
	channelKeyCache.Set(key.ID, key)
	channelKeyCacheNeedUpdate.mark(key.ID)
	return nil
}

// ChannelKeyCredentialReplace durably persists a rotated credential before it
// updates the in-memory caches. The compare-and-swap predicate prevents an
// OAuth refresh from overwriting a concurrent administrator key replacement.
func ChannelKeyCredentialReplace(ctx context.Context, channelID, keyID int, previous, next string) error {
	previous = strings.TrimSpace(previous)
	next = strings.TrimSpace(next)
	if channelID <= 0 || keyID <= 0 || previous == "" || next == "" || len(next) > model.MaxChannelKeyBytes {
		return fmt.Errorf("%w: invalid channel credential replacement", ErrInvalidInput)
	}
	if previous == next {
		return nil
	}
	channelKeyPersistenceMu.Lock()
	defer channelKeyPersistenceMu.Unlock()

	result := db.GetDB().WithContext(ctx).Model(&model.ChannelKey{}).
		Where("id = ? AND channel_id = ? AND channel_key = ?", keyID, channelID, previous).
		Update("channel_key", next)
	if result.Error != nil {
		return fmt.Errorf("persist refreshed channel credential: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: channel credential changed concurrently", ErrConflict)
	}

	if cached, ok := channelKeyCache.Get(keyID); ok && cached.ChannelID == channelID && cached.ChannelKey == previous {
		cached.ChannelKey = next
		channelKeyCache.Set(keyID, cached)
	}
	if channel, ok := channelCache.Get(channelID); ok {
		keys := append([]model.ChannelKey(nil), channel.Keys...)
		for i := range keys {
			if keys[i].ID == keyID && keys[i].ChannelKey == previous {
				keys[i].ChannelKey = next
				channel.Keys = keys
				channelCache.Set(channelID, channel)
				break
			}
		}
	}
	return nil
}
func ChannelBaseUrlUpdate(channelID int, baseUrl []model.BaseUrl) error {
	ch, ok := channelCache.Get(channelID)
	if !ok {
		return fmt.Errorf("channel not found")
	}
	// Copy to decouple callers from internal cache storage.
	if baseUrl == nil {
		ch.BaseUrls = nil
	} else {
		cp := make([]model.BaseUrl, len(baseUrl))
		copy(cp, baseUrl)
		ch.BaseUrls = cp
	}
	channelCache.Set(channelID, ch)
	return nil
}

// ChannelKeySaveDB 将运行时更新过的 ChannelKey 缓存写入数据库。
func ChannelKeySaveDB(ctx context.Context) error {
	channelKeyPersistenceMu.Lock()
	defer channelKeyPersistenceMu.Unlock()
	// Snapshot versions are acknowledged only after every write succeeds. A
	// concurrent ChannelKeyUpdate advances its version and therefore survives
	// clearUnchanged for the next retry.
	dirtySnapshot := channelKeyCacheNeedUpdate.snapshot()
	keyIDs := dirtyIDs(dirtySnapshot)
	if len(keyIDs) == 0 {
		return nil
	}

	dbConn := db.GetDB().WithContext(ctx)
	for _, id := range keyIDs {
		k, ok := channelKeyCache.Get(id)
		if !ok {
			continue
		}
		if err := dbConn.Save(&k).Error; err != nil {
			return fmt.Errorf("save channel key %d: %w", id, err)
		}
	}
	channelKeyCacheNeedUpdate.clearUnchanged(dirtySnapshot)
	return nil
}

func ChannelUpdate(req *model.ChannelUpdateRequest, ctx context.Context) (*model.Channel, error) {
	return channelUpdate(req, nil, ctx)
}

// ChannelUpdateExpectedVersion atomically claims and updates one channel
// configuration version. It is used by self-healing apply/rollback so a stale
// diagnosis can never overwrite a concurrent administrator edit.
func ChannelUpdateExpectedVersion(req *model.ChannelUpdateRequest, expectedVersion int, ctx context.Context) (*model.Channel, error) {
	if expectedVersion <= 0 {
		return nil, fmt.Errorf("%w: expected channel config version must be positive", ErrInvalidInput)
	}
	return channelUpdate(req, &expectedVersion, ctx)
}

func channelUpdate(req *model.ChannelUpdateRequest, expectedVersion *int, ctx context.Context) (*model.Channel, error) {
	if err := model.ValidateChannelUpdate(req); err != nil {
		return nil, fmt.Errorf("%w: invalid channel update: %v", ErrInvalidInput, err)
	}
	currentChannel, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("%w: channel not found", ErrNotFound)
	}
	if err := validateChannelAuthenticationUpdate(currentChannel, req); err != nil {
		return nil, fmt.Errorf("%w: invalid channel update: %v", ErrInvalidInput, err)
	}
	if req.Name != nil {
		for id, existing := range channelCache.GetAll() {
			if id != req.ID && existing.Name == *req.Name {
				return nil, fmt.Errorf("%w: channel name already exists", ErrConflict)
			}
		}
	}

	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()
	if expectedVersion != nil {
		result := tx.Model(&model.Channel{}).
			Where("id = ? AND config_version = ?", req.ID, *expectedVersion).
			UpdateColumn("config_version", gorm.Expr("config_version + ?", 1))
		if result.Error != nil {
			tx.Rollback()
			return nil, fmt.Errorf("claim channel config version: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			tx.Rollback()
			return nil, fmt.Errorf("%w: channel configuration changed concurrently", ErrConflict)
		}
	}

	if err := applyChannelPatchTx(tx, req); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := deleteChannelKeysTx(tx, req.ID, req.KeysToDelete); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := updateChannelKeysTx(tx, req.ID, req.KeysToUpdate); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := addChannelKeysTx(tx, req.ID, req.KeysToAdd); err != nil {
		tx.Rollback()
		return nil, err
	}
	if expectedVersion == nil && channelUpdateHasChanges(req) {
		if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).
			UpdateColumn("config_version", gorm.Expr("config_version + ?", 1)).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("increment channel config version: %w", err)
		}
	}
	if err := invalidateCapabilityEvidenceForChannelUpdateTx(tx, req); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("invalidate channel capability evidence: %w", err)
	}
	if tx.Migrator().HasTable(&model.ChannelBaseline{}) {
		if err := deleteChannelBaselinesKeysTx(tx, req.ID, req.KeysToDelete); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("delete removed-key channel baselines: %w", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 刷新缓存并返回最新数据
	if err := channelRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, err
	}

	channel, _ := channelCache.Get(req.ID)
	return &channel, nil
}

func validateChannelAuthenticationUpdate(current model.Channel, req *model.ChannelUpdateRequest) error {
	channelType := current.Type
	if req.Type != nil {
		channelType = *req.Type
	}
	baseURLs := current.BaseUrls
	if req.BaseUrls != nil {
		baseURLs = *req.BaseUrls
	}

	keysByID := make(map[int]model.ChannelKey, len(current.Keys))
	for _, key := range current.Keys {
		keysByID[key.ID] = key
	}
	for _, id := range req.KeysToDelete {
		delete(keysByID, id)
	}
	for _, update := range req.KeysToUpdate {
		key, exists := keysByID[update.ID]
		if !exists {
			continue
		}
		if update.ChannelKey != nil {
			key.ChannelKey = strings.TrimSpace(*update.ChannelKey)
		}
		keysByID[update.ID] = key
	}
	keys := make([]model.ChannelKey, 0, len(keysByID)+len(req.KeysToAdd))
	for _, key := range keysByID {
		keys = append(keys, key)
	}
	for _, key := range req.KeysToAdd {
		keys = append(keys, model.ChannelKey{Enabled: key.Enabled, ChannelKey: strings.TrimSpace(key.ChannelKey), Remark: key.Remark})
	}
	return model.ValidateChannelAuthentication(channelType, baseURLs, keys)
}

func applyChannelPatchTx(tx *gorm.DB, req *model.ChannelUpdateRequest) error {
	helper := NewPatchHelper()
	helper.ApplyField("name", req.Name)
	helper.ApplyField("type", req.Type)
	helper.ApplyField("enabled", req.Enabled)
	if err := applyChannelJSONPatchField(helper, "base_urls", req.BaseUrls); err != nil {
		return err
	}
	helper.ApplyField("model", req.Model)
	helper.ApplyField("custom_model", req.CustomModel)
	helper.ApplyField("proxy", req.Proxy)
	helper.ApplyField("auto_sync", req.AutoSync)
	helper.ApplyField("auto_group", req.AutoGroup)
	if err := applyChannelJSONPatchField(helper, "custom_header", req.CustomHeader); err != nil {
		return err
	}
	if err := applyChannelJSONPatchField(helper, "header_rules", req.HeaderRules); err != nil {
		return err
	}
	if err := applyChannelJSONPatchField(helper, "json_rewrite_rules", req.JSONRewriteRules); err != nil {
		return err
	}
	helper.ApplyField("channel_proxy", req.ChannelProxy)
	helper.ApplyField("param_override", req.ParamOverride)
	helper.ApplyField("raw_passthrough", req.RawPassthrough)
	helper.ApplyField("rpm_limit", req.RPMLimit)
	helper.ApplyField("max_concurrency", req.MaxConcurrency)
	helper.ApplyField("match_regex", req.MatchRegex)
	helper.ApplyField("user_agent", req.UserAgent)
	helper.ApplyField("policy_profile", req.PolicyProfile)
	helper.ApplyField("self_healing_enabled", req.SelfHealingEnabled)

	if !helper.HasUpdates() {
		return nil
	}
	result := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Select(helper.SelectFields()).Updates(helper.Updates())
	if result.Error != nil {
		return fmt.Errorf("failed to update channel: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := tx.Model(&model.Channel{}).Where("id = ?", req.ID).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify channel update: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("%w: channel not found", ErrNotFound)
		}
	}
	return nil
}

func channelUpdateHasChanges(req *model.ChannelUpdateRequest) bool {
	if req == nil {
		return false
	}
	return req.Name != nil || req.Type != nil || req.Enabled != nil || req.BaseUrls != nil ||
		req.Model != nil || req.CustomModel != nil || req.Proxy != nil || req.AutoSync != nil ||
		req.AutoGroup != nil || req.CustomHeader != nil || req.HeaderRules != nil ||
		req.JSONRewriteRules != nil || req.ChannelProxy != nil || req.ParamOverride != nil ||
		req.RawPassthrough != nil || req.RPMLimit != nil || req.MaxConcurrency != nil ||
		req.MatchRegex != nil || req.UserAgent != nil || req.PolicyProfile != nil ||
		req.SelfHealingEnabled != nil || len(req.KeysToAdd) > 0 || len(req.KeysToUpdate) > 0 || len(req.KeysToDelete) > 0
}

// GORM serializers are not invoked for map-based Updates values. Encode JSON
// slice patches explicitly so SQLite, PostgreSQL, and MySQL receive the same
// representation used by serializer:json on create/read paths.
func applyChannelJSONPatchField[T any](helper *PatchHelper, field string, value *[]T) error {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(*value)
	if err != nil {
		return fmt.Errorf("encode channel %s patch: %w", field, err)
	}
	helper.ApplyField(field, string(encoded))
	return nil
}

func deleteChannelKeysTx(tx *gorm.DB, channelID int, keyIDs []int) error {
	if len(keyIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&model.ChannelKey{}).Where("id IN ? AND channel_id = ?", keyIDs, channelID).Count(&count).Error; err != nil {
		return fmt.Errorf("failed to verify channel keys to delete: %w", err)
	}
	if count != int64(len(keyIDs)) {
		return fmt.Errorf("%w: one or more channel keys to delete were not found", ErrNotFound)
	}
	if err := tx.Where("id IN ? AND channel_id = ?", keyIDs, channelID).Delete(&model.ChannelKey{}).Error; err != nil {
		return fmt.Errorf("failed to delete channel keys: %w", err)
	}
	return nil
}

func updateChannelKeysTx(tx *gorm.DB, channelID int, keys []model.ChannelKeyUpdateRequest) error {
	for _, key := range keys {
		updates := channelKeyUpdates(key)
		if len(updates) == 0 {
			continue
		}
		result := tx.Model(&model.ChannelKey{}).
			Where("id = ? AND channel_id = ?", key.ID, channelID).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("failed to update channel key %d: %w", key.ID, result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&model.ChannelKey{}).Where("id = ? AND channel_id = ?", key.ID, channelID).Count(&count).Error; err != nil {
				return fmt.Errorf("failed to verify channel key %d update: %w", key.ID, err)
			}
			if count != 1 {
				return fmt.Errorf("%w: channel key %d not found", ErrNotFound, key.ID)
			}
		}
	}
	return nil
}

func channelKeyUpdates(key model.ChannelKeyUpdateRequest) map[string]interface{} {
	updates := make(map[string]interface{}, 5)
	resetRuntimeState := false
	if key.Enabled != nil {
		updates["enabled"] = *key.Enabled
		resetRuntimeState = *key.Enabled
	}
	if key.ChannelKey != nil {
		updates["channel_key"] = *key.ChannelKey
		resetRuntimeState = true
	}
	if key.Remark != nil {
		updates["remark"] = *key.Remark
	}
	if resetRuntimeState {
		updates["status_code"] = 0
		updates["last_use_time_stamp"] = 0
		updates["retry_after_until"] = 0
	}
	return updates
}

func addChannelKeysTx(tx *gorm.DB, channelID int, keys []model.ChannelKeyAddRequest) error {
	if len(keys) == 0 {
		return nil
	}
	newKeys := make([]model.ChannelKey, 0, len(keys))
	for _, key := range keys {
		newKeys = append(newKeys, model.ChannelKey{
			ChannelID:  channelID,
			Enabled:    key.Enabled,
			ChannelKey: key.ChannelKey,
			Remark:     key.Remark,
		})
	}
	if err := tx.Create(&newKeys).Error; err != nil {
		return fmt.Errorf("failed to create channel keys: %w", err)
	}
	return nil
}

func invalidateCapabilityEvidenceForChannelUpdateTx(tx *gorm.DB, req *model.ChannelUpdateRequest) error {
	if !tx.Migrator().HasTable(&model.CapabilityEvidence{}) {
		return nil
	}
	if req.Type != nil || req.BaseUrls != nil || req.Model != nil || req.CustomModel != nil ||
		req.Proxy != nil || req.ChannelProxy != nil || req.CustomHeader != nil ||
		req.HeaderRules != nil || req.JSONRewriteRules != nil || req.ParamOverride != nil ||
		req.UserAgent != nil || req.RawPassthrough != nil {
		return deleteCapabilityEvidenceChannelTx(tx, req.ID)
	}
	keyIDs := append([]int(nil), req.KeysToDelete...)
	for _, key := range req.KeysToUpdate {
		if key.ChannelKey != nil || key.Enabled != nil {
			keyIDs = append(keyIDs, key.ID)
		}
	}
	return deleteCapabilityEvidenceKeysTx(tx, req.ID, keyIDs)
}

func ChannelEnabled(id int, enabled bool, ctx context.Context) error {
	oldChannel, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("%w: channel not found", ErrNotFound)
	}
	result := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"enabled": enabled, "config_version": gorm.Expr("config_version + ?", 1),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := db.GetDB().WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("failed to verify channel enable update: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("%w: channel not found", ErrNotFound)
		}
	}
	oldChannel.Enabled = enabled
	oldChannel.ConfigVersion++
	channelCache.Set(id, oldChannel)
	return nil
}

func ChannelDel(id int, ctx context.Context) error {
	ch, ok := channelCache.Get(id)
	if !ok {
		return fmt.Errorf("%w: channel not found", ErrNotFound)
	}

	// 开启事务
	tx := db.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// 获取所有受影响的 GroupID，用于刷新缓存
	var affectedGroupIDs []int
	if err := tx.Model(&model.GroupItem{}).
		Where("channel_id = ?", id).
		Pluck("group_id", &affectedGroupIDs).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to get affected groups: %w", err)
	}

	// 删除所有引用该渠道的 GroupItem
	if err := tx.Where("channel_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	// 删除渠道 keys
	if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelKey{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel keys: %w", err)
	}

	if tx.Migrator().HasTable(&model.CapabilityEvidence{}) {
		if err := deleteCapabilityEvidenceChannelTx(tx, id); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to delete channel capability evidence: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.ChannelBaseline{}) {
		if err := deleteChannelBaselinesChannelTx(tx, id); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to delete channel baselines: %w", err)
		}
	}
	if err := deleteSelfHealingChannelTx(tx, id); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel self-healing evidence: %w", err)
	}

	// 删除统计数据
	if err := tx.Where("channel_id = ?", id).Delete(&model.StatsChannel{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel stats: %w", err)
	}

	// 删除渠道
	result := tx.Delete(&model.Channel{}, id)
	if result.Error != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete channel: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		tx.Rollback()
		return fmt.Errorf("%w: channel not found", ErrNotFound)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 删除缓存
	channelCache.Del(id)
	for _, k := range ch.Keys {
		if k.ID != 0 {
			channelKeyCache.Del(k.ID)
		}
	}
	if err := StatsChannelDel(id); err != nil {
		// The channel and its statistics were already deleted atomically above;
		// report a failure from the defensive post-commit cleanup without
		// returning a misleading rollback-style error to the caller.
		log.Warnf("channel %d deleted but post-commit stats cleanup failed: %v", id, err)
	}

	// 刷新受影响的分组缓存
	for _, groupID := range affectedGroupIDs {
		if err := groupRefreshCacheByID(groupID, ctx); err != nil {
			log.Warnf("failed to refresh group cache for group %d: %v", groupID, err)
		}
	}

	return nil
}

func ChannelLLMList(ctx context.Context) ([]model.LLMChannel, error) {
	return channelService.LLMList(ctx)
}

func (s *ChannelService) LLMList(ctx context.Context) ([]model.LLMChannel, error) {
	models := []model.LLMChannel{}
	for _, channel := range s.channels.GetAll() {
		modelNames := xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel)
		for _, modelName := range modelNames {
			if modelName == "" {
				continue
			}
			channelName := channel.Name
			if channelName == "" {
				channelName = fmt.Sprintf("Channel %d", channel.ID)
			}
			models = append(models, model.LLMChannel{
				Name:        modelName,
				Enabled:     channel.Enabled,
				ChannelID:   channel.ID,
				ChannelName: channelName,
			})
		}
	}
	return models, nil
}

func ChannelGet(id int, ctx context.Context) (*model.Channel, error) {
	return channelService.Get(id, ctx)
}

func (s *ChannelService) Get(id int, ctx context.Context) (*model.Channel, error) {
	channel, ok := s.channels.Get(id)
	if !ok {
		return nil, fmt.Errorf("channel not found")
	}
	return &channel, nil
}

func channelRefreshCache(ctx context.Context) error {
	channels := []model.Channel{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Preload("Stats").
		Find(&channels).Error; err != nil {
		log.Warnf("failed to get channels: %v", err)
		return err
	}
	channelCache.Clear()
	channelKeyCache.Clear()
	channelKeyCacheNeedUpdate.reset()
	for _, channel := range channels {
		channelCache.Set(channel.ID, channel)
		for _, k := range channel.Keys {
			if k.ID != 0 {
				channelKeyCache.Set(k.ID, k)
			}
		}
	}
	return nil
}

func channelRefreshCacheByID(id int, ctx context.Context) error {
	if old, ok := channelCache.Get(id); ok {
		for _, k := range old.Keys {
			if k.ID != 0 {
				channelKeyCache.Del(k.ID)
			}
		}
	}
	var channel model.Channel
	if err := db.GetDB().WithContext(ctx).
		Preload("Keys").
		Preload("Stats").
		First(&channel, id).Error; err != nil {
		return err
	}
	channelCache.Set(channel.ID, channel)
	for _, k := range channel.Keys {
		if k.ID != 0 {
			channelKeyCache.Set(k.ID, k)
		}
	}
	return nil
}
