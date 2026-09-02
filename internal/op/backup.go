package op

import (
	"context"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dbDumpVersion = 3
const configDumpVersion = 4

// DBExportAll 导出完整数据库内容，包括所有统计数据。
func DBExportAll(ctx context.Context) (*model.DBDump, error) {
	conn := db.GetDB().WithContext(ctx)

	d := &model.DBDump{
		Version:    dbDumpVersion,
		ExportedAt: time.Now().UTC(),
	}

	if err := conn.Find(&d.Channels).Error; err != nil {
		return nil, fmt.Errorf("export channels: %w", err)
	}
	if conn.Migrator().HasTable(&model.ChannelKey{}) {
		if err := conn.Find(&d.ChannelKeys).Error; err != nil {
			return nil, fmt.Errorf("export channel_keys: %w", err)
		}
	}
	if err := conn.Find(&d.Groups).Error; err != nil {
		return nil, fmt.Errorf("export groups: %w", err)
	}
	if err := conn.Find(&d.ChannelModels).Error; err != nil {
		return nil, fmt.Errorf("export channel_models: %w", err)
	}
	if err := conn.Find(&d.GroupItems).Error; err != nil {
		return nil, fmt.Errorf("export group_items: %w", err)
	}
	if err := conn.Find(&d.LLMInfos).Error; err != nil {
		return nil, fmt.Errorf("export llm_infos: %w", err)
	}
	if err := conn.Find(&d.APIKeys).Error; err != nil {
		return nil, fmt.Errorf("export api_keys: %w", err)
	}
	if err := conn.Find(&d.Settings).Error; err != nil {
		return nil, fmt.Errorf("export settings: %w", err)
	}
	d.Settings = withoutSecretSettings(d.Settings)

	if err := conn.Find(&d.StatsTotal).Error; err != nil {
		return nil, fmt.Errorf("export stats_total: %w", err)
	}
	if err := conn.Find(&d.StatsDaily).Error; err != nil {
		return nil, fmt.Errorf("export stats_daily: %w", err)
	}
	if err := conn.Find(&d.StatsHourly).Error; err != nil {
		return nil, fmt.Errorf("export stats_hourly: %w", err)
	}
	if err := conn.Find(&d.StatsAPIKey).Error; err != nil {
		return nil, fmt.Errorf("export stats_api_key: %w", err)
	}

	return d, nil
}

// DBExportConfig 导出跨部署迁移所需的配置，不包含日志、统计或模型价格缓存。
// 使用独立的 wire 类型，避免 Channel/ChannelModel 内嵌的 StatsMetrics 被序列化。
func DBExportConfig(ctx context.Context) (*model.ConfigDump, error) {
	conn := db.GetDB().WithContext(ctx)
	d := &model.ConfigDump{
		Version:    configDumpVersion,
		Scope:      model.ConfigDumpScope,
		ExportedAt: time.Now().UTC(),
	}

	channels := make([]model.Channel, 0)
	if err := conn.Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("export config channels: %w", err)
	}
	d.Channels = make([]model.ConfigChannel, 0, len(channels))
	keys := make([]model.ChannelKey, 0)
	if conn.Migrator().HasTable(&model.ChannelKey{}) {
		if err := conn.Find(&keys).Error; err != nil {
			return nil, fmt.Errorf("export config channel_keys: %w", err)
		}
	}
	keysByChannel := make(map[int][]model.ConfigChannelKey, len(channels))
	for _, key := range keys {
		keysByChannel[key.ChannelID] = append(keysByChannel[key.ChannelID], model.ConfigChannelKey(key))
	}
	for _, channel := range channels {
		baseURLs := append([]model.BaseUrl(nil), channel.BaseUrls...)
		if len(baseURLs) == 0 && channel.BaseURL != "" {
			baseURLs = []model.BaseUrl{{URL: channel.BaseURL}}
		}
		channelKeys := keysByChannel[channel.ID]
		if len(channelKeys) == 0 && channel.Key != "" {
			channelKeys = []model.ConfigChannelKey{{ChannelID: channel.ID, Enabled: true, ChannelKey: channel.Key}}
		}
		d.Channels = append(d.Channels, model.ConfigChannel{
			ID:               channel.ID,
			Name:             channel.Name,
			Type:             channel.Type,
			Enabled:          channel.Enabled,
			BaseURL:          channel.BaseURL,
			BaseUrls:         baseURLs,
			Key:              channel.Key,
			Keys:             channelKeys,
			Proxy:            channel.Proxy,
			AutoSync:         channel.AutoSync,
			CustomHeader:     channel.CustomHeader,
			HeaderRules:      channel.HeaderRules,
			JSONRewriteRules: channel.JSONRewriteRules,
			ParamOverride:    channel.ParamOverride,
			ChannelProxy:     channel.ChannelProxy,
			MatchRegex:       channel.MatchRegex,
		})
	}

	channelModels := make([]model.ChannelModel, 0)
	if err := conn.Find(&channelModels).Error; err != nil {
		return nil, fmt.Errorf("export config channel_models: %w", err)
	}
	d.ChannelModels = make([]model.ConfigChannelModel, 0, len(channelModels))
	for _, channelModel := range channelModels {
		d.ChannelModels = append(d.ChannelModels, model.ConfigChannelModel{
			ID:        channelModel.ID,
			ChannelID: channelModel.ChannelID,
			Name:      channelModel.Name,
			Source:    channelModel.Source,
		})
	}

	groups := make([]model.Group, 0)
	if err := conn.Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("export config groups: %w", err)
	}
	d.Groups = make([]model.ConfigGroup, 0, len(groups))
	for _, group := range groups {
		d.Groups = append(d.Groups, model.ConfigGroup{
			ID:           group.ID,
			Name:         group.Name,
			Enabled:      group.Enabled,
			Mode:         group.Mode,
			ActiveItemID: group.ActiveItemID,
			RelayConfig:  group.RelayConfig,
		})
	}

	groupItems := make([]model.GroupItem, 0)
	if err := conn.Find(&groupItems).Error; err != nil {
		return nil, fmt.Errorf("export config group_items: %w", err)
	}
	d.GroupItems = make([]model.ConfigGroupItem, 0, len(groupItems))
	for _, item := range groupItems {
		weight := item.Weight
		if weight <= 0 {
			weight = 1
		}
		d.GroupItems = append(d.GroupItems, model.ConfigGroupItem{
			ID:             item.ID,
			GroupID:        item.GroupID,
			Type:           item.Type,
			ChannelModelID: item.ChannelModelID,
			TargetGroupID:  item.TargetGroupID,
			Priority:       item.Priority,
			Weight:         weight,
			Disabled:       item.Disabled,
		})
	}

	if err := conn.Find(&d.APIKeys).Error; err != nil {
		return nil, fmt.Errorf("export config api_keys: %w", err)
	}
	if err := conn.Find(&d.Settings).Error; err != nil {
		return nil, fmt.Errorf("export config settings: %w", err)
	}
	d.Settings = withoutSecretSettings(d.Settings)
	return d, nil
}

func DBImportIncremental(ctx context.Context, dump *model.DBDump) (*model.DBImportResult, error) {
	if dump == nil {
		return nil, fmt.Errorf("empty dump")
	}
	groupMutationMu.Lock()
	defer groupMutationMu.Unlock()

	if dump.Version != 0 && dump.Version != dbDumpVersion {
		return nil, fmt.Errorf("unsupported dump version: %d", dump.Version)
	}

	conn := db.GetDB().WithContext(ctx)
	res := &model.DBImportResult{RowsAffected: map[string]int64{}}
	configOnly := dump.Scope == model.ConfigDumpScope
	err := conn.Transaction(func(tx *gorm.DB) error {
		// base tables
		if n, err := createDoNothing(tx, dump.Channels); err != nil {
			return fmt.Errorf("import channels: %w", err)
		} else {
			res.RowsAffected["channels"] = n
		}
		if n, err := createDoNothing(tx, dump.ChannelKeys); err != nil {
			return fmt.Errorf("import channel_keys: %w", err)
		} else {
			res.RowsAffected["channel_keys"] = n
		}
		if !configOnly {
			for _, channel := range dump.Channels {
				if err := tx.Model(&model.Channel{}).
					Where("id = ?", channel.ID).
					Select("input_token", "output_token", "input_cost", "output_cost", "wait_time", "request_success", "request_failed").
					Updates(&channel).Error; err != nil {
					return fmt.Errorf("import channel stats: %w", err)
				}
			}
		}
		if n, err := createDoNothing(tx, dump.Groups); err != nil {
			return fmt.Errorf("import groups: %w", err)
		} else {
			res.RowsAffected["groups"] = n
		}
		if n, err := createUpsertChannelModels(tx, dump.ChannelModels, configOnly); err != nil {
			return fmt.Errorf("import channel_models: %w", err)
		} else {
			res.RowsAffected["channel_models"] = n
		}
		if n, err := createDoNothing(tx, dump.GroupItems); err != nil {
			return fmt.Errorf("import group_items: %w", err)
		} else {
			res.RowsAffected["group_items"] = n
		}
		// Imports can contain nested group members as well as the regular
		// channel-model members. Validate the complete graph before committing so
		// an import cannot introduce self references, cycles, missing targets, or
		// a nesting depth the relay cannot safely expand.
		if err := validateGroupGraph(tx); err != nil {
			return fmt.Errorf("import group graph: %w", err)
		}
		if n, err := createUpsertAll(tx, dump.LLMInfos, []clause.Column{{Name: "name"}}); err != nil {
			return fmt.Errorf("import llm_infos: %w", err)
		} else {
			res.RowsAffected["llm_infos"] = n
		}
		if n, err := createDoNothing(tx, dump.APIKeys); err != nil {
			return fmt.Errorf("import api_keys: %w", err)
		} else {
			res.RowsAffected["api_keys"] = n
		}
		if n, err := createUpsertSettings(tx, dump.Settings); err != nil {
			return fmt.Errorf("import settings: %w", err)
		} else {
			res.RowsAffected["settings"] = n
		}

		if configOnly {
			return nil
		}
		if n, err := createUpsertAll(tx, dump.StatsTotal, []clause.Column{{Name: "id"}}); err != nil {
			return fmt.Errorf("import stats_total: %w", err)
		} else {
			res.RowsAffected["stats_total"] = n
		}
		if n, err := createUpsertAll(tx, dump.StatsDaily, []clause.Column{{Name: "date"}}); err != nil {
			return fmt.Errorf("import stats_daily: %w", err)
		} else {
			res.RowsAffected["stats_daily"] = n
		}
		if n, err := createUpsertAll(tx, dump.StatsHourly, []clause.Column{{Name: "hour"}}); err != nil {
			return fmt.Errorf("import stats_hourly: %w", err)
		} else {
			res.RowsAffected["stats_hourly"] = n
		}
		if n, err := createUpsertAll(tx, dump.StatsAPIKey, []clause.Column{{Name: "api_key_id"}}); err != nil {
			return fmt.Errorf("import stats_api_key: %w", err)
		} else {
			res.RowsAffected["stats_api_key"] = n
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// DBImportConfig 导入仅配置备份。统计、日志和模型价格不在 ConfigDump 类型中，
// 因此即使上传者手工追加这些字段，也不会进入导入路径。
func DBImportConfig(ctx context.Context, dump *model.ConfigDump) (*model.DBImportResult, error) {
	if dump == nil {
		return nil, fmt.Errorf("empty config dump")
	}
	if dump.Version != configDumpVersion {
		return nil, fmt.Errorf("unsupported config dump version: %d", dump.Version)
	}
	if dump.Scope != model.ConfigDumpScope {
		return nil, fmt.Errorf("unsupported config dump scope: %q", dump.Scope)
	}

	converted := &model.DBDump{
		Version:       dbDumpVersion,
		Scope:         model.ConfigDumpScope,
		ExportedAt:    dump.ExportedAt,
		Channels:      make([]model.Channel, 0, len(dump.Channels)),
		ChannelKeys:   make([]model.ChannelKey, 0),
		ChannelModels: make([]model.ChannelModel, 0, len(dump.ChannelModels)),
		Groups:        make([]model.Group, 0, len(dump.Groups)),
		GroupItems:    make([]model.GroupItem, 0, len(dump.GroupItems)),
		APIKeys:       dump.APIKeys,
		Settings:      dump.Settings,
	}
	var rewriteWarnings []string
	for _, channel := range dump.Channels {
		baseURLs := append([]model.BaseUrl(nil), channel.BaseUrls...)
		baseURL := channel.BaseURL
		if baseURL == "" && len(baseURLs) > 0 {
			baseURL = baseURLs[0].URL
		}
		keys := append([]model.ConfigChannelKey(nil), channel.Keys...)
		key := channel.Key
		if key == "" && len(keys) > 0 {
			key = keys[0].ChannelKey
		}
		// 导入文件不可信：非法改写规则在此丢弃并告警，避免绕过实时编辑的校验。
		headerRules := channel.HeaderRules
		if err := validateHeaderRules(headerRules); err != nil {
			rewriteWarnings = append(rewriteWarnings, fmt.Sprintf("channel %q header rules dropped: %v", channel.Name, err))
			headerRules = nil
		}
		jsonRewriteRules := channel.JSONRewriteRules
		if err := validateJSONRewriteRules(jsonRewriteRules); err != nil {
			rewriteWarnings = append(rewriteWarnings, fmt.Sprintf("channel %q json rewrite rules dropped: %v", channel.Name, err))
			jsonRewriteRules = nil
		}
		converted.Channels = append(converted.Channels, model.Channel{
			ID:               channel.ID,
			Name:             channel.Name,
			Type:             channel.Type,
			Enabled:          channel.Enabled,
			BaseURL:          baseURL,
			BaseUrls:         baseURLs,
			Key:              key,
			Proxy:            channel.Proxy,
			AutoSync:         channel.AutoSync,
			CustomHeader:     channel.CustomHeader,
			HeaderRules:      headerRules,
			JSONRewriteRules: jsonRewriteRules,
			ParamOverride:    channel.ParamOverride,
			ChannelProxy:     channel.ChannelProxy,
			MatchRegex:       channel.MatchRegex,
		})
		for _, channelKey := range keys {
			channelKeyID := channelKey.ChannelID
			if channelKeyID == 0 {
				channelKeyID = channel.ID
			}
			converted.ChannelKeys = append(converted.ChannelKeys, model.ChannelKey{
				ID:               channelKey.ID,
				ChannelID:        channelKeyID,
				Enabled:          channelKey.Enabled,
				ChannelKey:       channelKey.ChannelKey,
				StatusCode:       channelKey.StatusCode,
				LastUseTimeStamp: channelKey.LastUseTimeStamp,
				RetryAfterUntil:  channelKey.RetryAfterUntil,
				Remark:           channelKey.Remark,
			})
		}
	}
	for _, channelModel := range dump.ChannelModels {
		converted.ChannelModels = append(converted.ChannelModels, model.ChannelModel{
			ID:        channelModel.ID,
			ChannelID: channelModel.ChannelID,
			Name:      channelModel.Name,
			Source:    channelModel.Source,
		})
	}
	for _, group := range dump.Groups {
		converted.Groups = append(converted.Groups, model.Group{
			ID:           group.ID,
			Name:         group.Name,
			Enabled:      group.Enabled,
			Mode:         group.Mode,
			ActiveItemID: group.ActiveItemID,
			RelayConfig:  group.RelayConfig,
		})
	}
	for _, item := range dump.GroupItems {
		weight := item.Weight
		if weight <= 0 {
			weight = 1
		}
		converted.GroupItems = append(converted.GroupItems, model.GroupItem{
			ID:             item.ID,
			GroupID:        item.GroupID,
			Type:           item.Type,
			ChannelModelID: item.ChannelModelID,
			TargetGroupID:  item.TargetGroupID,
			Priority:       item.Priority,
			Weight:         weight,
			Disabled:       item.Disabled,
		})
	}
	result, err := DBImportIncremental(ctx, converted)
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, dump.Warnings...)
	result.Warnings = append(result.Warnings, rewriteWarnings...)
	return result, nil
}

func createUpsertChannelModels(tx *gorm.DB, rows []model.ChannelModel, configOnly bool) (int64, error) {
	if configOnly {
		if len(rows) == 0 {
			return 0, nil
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"channel_id", "name", "source"}),
		}).CreateInBatches(&rows, batchSize)
		return result.RowsAffected, result.Error
	}
	return createUpsertAll(tx, rows, []clause.Column{{Name: "id"}})
}

// batchSize 控制每次 INSERT 的最大行数。
// 单行字段数较多（如 stats_hourly 含 9 个字段），若一次插入过多行会超过数据库绑定参数上限（SQLite/PostgreSQL 为 65535），按行数分批写入可规避该限制。
const batchSize = 2000

func createDoNothing[T any](tx *gorm.DB, rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, batchSize)
	return result.RowsAffected, result.Error
}

func createUpsertAll[T any](tx *gorm.DB, rows []T, columns []clause.Column) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   columns,
		UpdateAll: true,
	}).CreateInBatches(&rows, batchSize)
	return result.RowsAffected, result.Error
}

// withoutSecretSettings 过滤运行时凭据状态，导出与导入两侧共用。
func withoutSecretSettings(rows []model.Setting) []model.Setting {
	filtered := make([]model.Setting, 0, len(rows))
	for _, row := range rows {
		if row.Key.IsSecret() {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

// createUpsertSettings 丢弃备份中的敏感项。导入自带的 jwt_secret 会让攻击者
// 用已知密钥接管本实例，导入 token_version 则可能回退版本让旧 token 复活。
func createUpsertSettings(tx *gorm.DB, rows []model.Setting) (int64, error) {
	rows = withoutSecretSettings(rows)
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&rows)
	return result.RowsAffected, result.Error
}
