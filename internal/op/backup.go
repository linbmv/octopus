package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	dbDumpLegacyVersion = 1
	dbDumpVersion       = 2
)

var dbRestoreMu sync.Mutex

func DBExportAllStream(ctx context.Context, w io.Writer, includeLogs, includeStats bool) error {
	conn := db.GetDB().WithContext(ctx)
	wroteField := false

	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	if err := writeDBDumpJSONField(w, &wroteField, "version", dbDumpVersion); err != nil {
		return err
	}
	if err := writeDBDumpJSONField(w, &wroteField, "exported_at", time.Now().UTC()); err != nil {
		return err
	}
	if err := writeDBDumpJSONField(w, &wroteField, "include_logs", includeLogs); err != nil {
		return err
	}
	if err := writeDBDumpJSONField(w, &wroteField, "include_stats", includeStats); err != nil {
		return err
	}

	if err := streamDBDumpTable[model.Channel](conn, w, &wroteField, "channels", "channels"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.ChannelKey](conn, w, &wroteField, "channel_keys", "channel_keys"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.Group](conn, w, &wroteField, "groups", "groups"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.GroupItem](conn, w, &wroteField, "group_items", "group_items"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.LLMInfo](conn, w, &wroteField, "llm_infos", "llm_infos"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.APIKey](conn, w, &wroteField, "api_keys", "api_keys"); err != nil {
		return err
	}
	if err := streamDBDumpTable[model.Setting](conn, w, &wroteField, "settings", "settings"); err != nil {
		return err
	}
	relations, err := buildDBDumpRelationsV2(conn)
	if err != nil {
		return err
	}
	if err := writeDBDumpJSONField(w, &wroteField, "relations", relations); err != nil {
		return err
	}

	// Users 表不导出：包含密码哈希、JWT 密钥等敏感凭据，泄露后可伪造令牌或暴力破解。
	// 恢复时由目标实例重新初始化管理员。

	if includeStats {
		if err := streamDBDumpTable[model.StatsTotal](conn, w, &wroteField, "stats_total", "stats_total"); err != nil {
			return err
		}
		if err := streamDBDumpTable[model.StatsDaily](conn, w, &wroteField, "stats_daily", "stats_daily"); err != nil {
			return err
		}
		if err := streamDBDumpTable[model.StatsHourly](conn, w, &wroteField, "stats_hourly", "stats_hourly"); err != nil {
			return err
		}
		if err := streamDBDumpTable[model.StatsChannel](conn, w, &wroteField, "stats_channel", "stats_channel"); err != nil {
			return err
		}
		if err := streamDBDumpTable[model.StatsChannelKey](conn, w, &wroteField, "stats_channel_key", "stats_channel_key"); err != nil {
			return err
		}
		if err := streamDBDumpTable[model.StatsAPIKey](conn, w, &wroteField, "stats_api_key", "stats_api_key"); err != nil {
			return err
		}
	}

	if includeLogs {
		if err := streamDBDumpTable[model.RelayLog](conn, w, &wroteField, "relay_logs", "relay_logs"); err != nil {
			return err
		}
	}

	_, err = io.WriteString(w, "}")
	return err
}

func buildDBDumpRelationsV2(conn *gorm.DB) (*model.DBDumpRelationsV2, error) {
	var channels []model.Channel
	if err := conn.Select("id", "uuid").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("export channel UUID relations: %w", err)
	}
	channelUUIDs := make(map[int]string, len(channels))
	for _, channel := range channels {
		channelUUIDs[channel.ID] = channel.UUID
	}
	var groups []model.Group
	if err := conn.Select("id", "uuid").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("export group UUID relations: %w", err)
	}
	groupUUIDs := make(map[int]string, len(groups))
	for _, group := range groups {
		groupUUIDs[group.ID] = group.UUID
	}
	var keys []model.ChannelKey
	if err := conn.Select("uuid", "channel_id").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("export channel key UUID relations: %w", err)
	}
	var items []model.GroupItem
	if err := conn.Select("uuid", "group_id", "type", "channel_id", "target_group_id").Find(&items).Error; err != nil {
		return nil, fmt.Errorf("export group item UUID relations: %w", err)
	}
	relations := &model.DBDumpRelationsV2{
		ChannelKeys: make(map[string]string, len(keys)),
		GroupItems:  make(map[string]model.DBDumpGroupItemRelation, len(items)),
	}
	for _, key := range keys {
		channelUUID, ok := channelUUIDs[key.ChannelID]
		if !ok || key.UUID == "" || channelUUID == "" {
			return nil, fmt.Errorf("export channel key %q references channel %d without a stable UUID", key.UUID, key.ChannelID)
		}
		relations.ChannelKeys[key.UUID] = channelUUID
	}
	for _, item := range items {
		relation := model.DBDumpGroupItemRelation{GroupUUID: groupUUIDs[item.GroupID]}
		switch item.Type {
		case model.GroupItemTypeChannel:
			relation.ChannelUUID = channelUUIDs[item.ChannelID]
		case model.GroupItemTypeGroup:
			relation.TargetGroupUUID = groupUUIDs[item.TargetGroupID]
		}
		if item.UUID == "" || relation.GroupUUID == "" || (relation.ChannelUUID == "" && relation.TargetGroupUUID == "") {
			return nil, fmt.Errorf("export group item %q has incomplete UUID relationships", item.UUID)
		}
		relations.GroupItems[item.UUID] = relation
	}
	return relations, nil
}

func writeDBDumpJSONField(w io.Writer, wroteField *bool, name string, value any) error {
	if err := writeDBDumpFieldPrefix(w, wroteField, name); err != nil {
		return err
	}
	return json.NewEncoder(w).Encode(value)
}

func streamDBDumpTable[T any](conn *gorm.DB, w io.Writer, wroteField *bool, name, table string) (err error) {
	if err := writeDBDumpFieldPrefix(w, wroteField, name); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}

	rows, err := conn.Model(new(T)).Rows()
	if err != nil {
		return fmt.Errorf("export %s: %w", table, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close exported %s rows: %w", table, closeErr))
		}
	}()

	encoder := json.NewEncoder(w)
	firstRow := true
	for rows.Next() {
		var item T
		if err := conn.ScanRows(rows, &item); err != nil {
			return fmt.Errorf("export %s: %w", table, err)
		}
		if !firstRow {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		firstRow = false
		if err := encoder.Encode(item); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("export %s: %w", table, err)
	}

	_, err = io.WriteString(w, "]")
	return err
}

func writeDBDumpFieldPrefix(w io.Writer, wroteField *bool, name string) error {
	if *wroteField {
		if _, err := io.WriteString(w, ","); err != nil {
			return err
		}
	}
	fieldName, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if _, err := w.Write(fieldName); err != nil {
		return err
	}
	if _, err := io.WriteString(w, ":"); err != nil {
		return err
	}
	*wroteField = true
	return nil
}

// DBImportRestore retains the empty-target restore contract for legacy v1 and
// for v2 callers that do not request an incremental strategy.
func DBImportRestore(ctx context.Context, dump *model.DBDump) (*model.DBImportResult, error) {
	if err := validateDBDump(dump); err != nil {
		return nil, err
	}

	dbRestoreMu.Lock()
	defer dbRestoreMu.Unlock()

	conn := db.GetDB().WithContext(ctx)
	res := &model.DBImportResult{
		Mode:         model.DBImportModeEmptyTargetRestore,
		RowsAffected: map[string]int64{},
	}

	err := conn.Transaction(func(tx *gorm.DB) error {
		if err := ensureRestoreTargetEmpty(tx); err != nil {
			return err
		}
		// Capability evidence is endpoint/account-specific runtime state, not
		// backup data. Clear any orphan rows in the same restore transaction.
		if tx.Migrator().HasTable(&model.CapabilityEvidence{}) {
			if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.CapabilityEvidence{}).Error; err != nil {
				return fmt.Errorf("clear capability evidence: %w", err)
			}
		}
		if err := ensureRestoreTargetSettingsValid(tx); err != nil {
			return err
		}

		if n, err := createRestoreRows(tx, dump.Channels); err != nil {
			return fmt.Errorf("import channels: %w", err)
		} else {
			res.RowsAffected["channels"] = n
		}
		if n, err := createRestoreRows(tx, dump.ChannelKeys); err != nil {
			return fmt.Errorf("import channel_keys: %w", err)
		} else {
			res.RowsAffected["channel_keys"] = n
		}
		if n, err := createRestoreRows(tx, dump.Groups); err != nil {
			return fmt.Errorf("import groups: %w", err)
		} else {
			res.RowsAffected["groups"] = n
		}
		if n, err := createRestoreRows(tx, dump.GroupItems); err != nil {
			return fmt.Errorf("import group_items: %w", err)
		} else {
			res.RowsAffected["group_items"] = n
		}
		if n, err := createRestoreRows(tx, dump.LLMInfos); err != nil {
			return fmt.Errorf("import llm_infos: %w", err)
		} else {
			res.RowsAffected["llm_infos"] = n
		}
		if n, err := createRestoreRows(tx, dump.APIKeys); err != nil {
			return fmt.Errorf("import api_keys: %w", err)
		} else {
			res.RowsAffected["api_keys"] = n
		}
		if n, err := createUpsertSettings(tx, dump.Settings); err != nil {
			return fmt.Errorf("import settings: %w", err)
		} else {
			res.RowsAffected["settings"] = n
		}

		// Users are intentionally neither exported nor imported. The target
		// instance retains its independently generated administrator credentials.

		if dump.IncludeStats {
			if n, err := createRestoreRows(tx, dump.StatsTotal); err != nil {
				return fmt.Errorf("import stats_total: %w", err)
			} else {
				res.RowsAffected["stats_total"] = n
			}
			if n, err := createRestoreRows(tx, dump.StatsDaily); err != nil {
				return fmt.Errorf("import stats_daily: %w", err)
			} else {
				res.RowsAffected["stats_daily"] = n
			}
			if n, err := createRestoreRows(tx, dump.StatsHourly); err != nil {
				return fmt.Errorf("import stats_hourly: %w", err)
			} else {
				res.RowsAffected["stats_hourly"] = n
			}
			if n, err := createRestoreRows(tx, dump.StatsChannel); err != nil {
				return fmt.Errorf("import stats_channel: %w", err)
			} else {
				res.RowsAffected["stats_channel"] = n
			}
			if n, err := createRestoreRows(tx, dump.StatsChannelKey); err != nil {
				return fmt.Errorf("import stats_channel_key: %w", err)
			} else {
				res.RowsAffected["stats_channel_key"] = n
			}
			if n, err := createRestoreRows(tx, dump.StatsAPIKey); err != nil {
				return fmt.Errorf("import stats_api_key: %w", err)
			} else {
				res.RowsAffected["stats_api_key"] = n
			}
		}

		if dump.IncludeLogs {
			if n, err := createRestoreRows(tx, dump.RelayLogs); err != nil {
				return fmt.Errorf("import relay_logs: %w", err)
			} else {
				res.RowsAffected["relay_logs"] = n
			}
		}

		return resetPostgresRestoreSequences(tx)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

var restoreTargetTables = []struct {
	name  string
	model any
}{
	{name: "channels", model: &model.Channel{}},
	{name: "channel_keys", model: &model.ChannelKey{}},
	{name: "groups", model: &model.Group{}},
	{name: "group_items", model: &model.GroupItem{}},
	{name: "llm_infos", model: &model.LLMInfo{}},
	{name: "api_keys", model: &model.APIKey{}},
	{name: "stats_total", model: &model.StatsTotal{}},
	{name: "stats_daily", model: &model.StatsDaily{}},
	{name: "stats_hourly", model: &model.StatsHourly{}},
	{name: "stats_channel", model: &model.StatsChannel{}},
	{name: "stats_channel_key", model: &model.StatsChannelKey{}},
	{name: "stats_api_key", model: &model.StatsAPIKey{}},
	{name: "relay_logs", model: &model.RelayLog{}},
}

func ensureRestoreTargetEmpty(tx *gorm.DB) error {
	for _, table := range restoreTargetTables {
		var count int64
		if err := tx.Model(table.model).Count(&count).Error; err != nil {
			return fmt.Errorf("check restore target table %s: %w", table.name, err)
		}
		if count != 0 {
			return invalidDump("target."+table.name, "must be empty for a version 1 restore (found %d rows)", count)
		}
	}
	return nil
}

// Settings use a stable key and a fresh instance normally contains defaults,
// so they are the only imported rows allowed to pre-exist. Invalid target rows
// still fail closed instead of being silently retained beside the backup.
func ensureRestoreTargetSettingsValid(tx *gorm.DB) error {
	var settings []model.Setting
	if err := tx.Find(&settings).Error; err != nil {
		return fmt.Errorf("check restore target settings: %w", err)
	}
	for i := range settings {
		if err := settings[i].Validate(); err != nil {
			return invalidDump("target.settings", "contains invalid row: %v", err)
		}
	}
	return nil
}

func createRestoreRows[T any](tx *gorm.DB, rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Omit(clause.Associations).CreateInBatches(&rows, 100)
	return result.RowsAffected, result.Error
}

func createUpsertSettings(tx *gorm.DB, rows []model.Setting) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&rows)
	return result.RowsAffected, result.Error
}

func resetPostgresRestoreSequences(tx *gorm.DB) error {
	if tx.Dialector == nil || tx.Name() != "postgres" {
		return nil
	}
	for _, table := range []string{"channels", "channel_keys", "groups", "group_items", "api_keys"} {
		// Table names are a fixed internal allowlist, not user-controlled input.
		query := fmt.Sprintf(
			"SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM %s",
			table,
			table,
		)
		if err := tx.Exec(query).Error; err != nil {
			return fmt.Errorf("reset %s id sequence: %w", table, err)
		}
	}
	return nil
}
