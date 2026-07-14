package op

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dbDumpVersion = 1

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
		if err := streamDBDumpTable[model.StatsAPIKey](conn, w, &wroteField, "stats_api_key", "stats_api_key"); err != nil {
			return err
		}
	}

	if includeLogs {
		if err := streamDBDumpTable[model.RelayLog](conn, w, &wroteField, "relay_logs", "relay_logs"); err != nil {
			return err
		}
	}

	_, err := io.WriteString(w, "}")
	return err
}

func writeDBDumpJSONField(w io.Writer, wroteField *bool, name string, value any) error {
	if err := writeDBDumpFieldPrefix(w, wroteField, name); err != nil {
		return err
	}
	return json.NewEncoder(w).Encode(value)
}

func streamDBDumpTable[T any](conn *gorm.DB, w io.Writer, wroteField *bool, name, table string) error {
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
	defer rows.Close()

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

func DBImportIncremental(ctx context.Context, dump *model.DBDump) (*model.DBImportResult, error) {
	if dump == nil {
		return nil, fmt.Errorf("empty dump")
	}

	if dump.Version != 0 && dump.Version != dbDumpVersion {
		return nil, fmt.Errorf("unsupported dump version: %d", dump.Version)
	}

	conn := db.GetDB().WithContext(ctx)
	res := &model.DBImportResult{RowsAffected: map[string]int64{}}

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
		if n, err := createDoNothing(tx, dump.Groups); err != nil {
			return fmt.Errorf("import groups: %w", err)
		} else {
			res.RowsAffected["groups"] = n
		}
		if n, err := createDoNothing(tx, dump.GroupItems); err != nil {
			return fmt.Errorf("import group_items: %w", err)
		} else {
			res.RowsAffected["group_items"] = n
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

		// Users 表不导入：备份不包含敏感凭据，恢复后由 UserInit 重新生成管理员。

		if dump.IncludeStats {
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
			if n, err := createUpsertAll(tx, dump.StatsChannel, []clause.Column{{Name: "channel_id"}}); err != nil {
				return fmt.Errorf("import stats_channel: %w", err)
			} else {
				res.RowsAffected["stats_channel"] = n
			}
			if n, err := createUpsertAll(tx, dump.StatsAPIKey, []clause.Column{{Name: "api_key_id"}}); err != nil {
				return fmt.Errorf("import stats_api_key: %w", err)
			} else {
				res.RowsAffected["stats_api_key"] = n
			}
		}

		if dump.IncludeLogs {
			if n, err := createDoNothing(tx, dump.RelayLogs); err != nil {
				return fmt.Errorf("import relay_logs: %w", err)
			} else {
				res.RowsAffected["relay_logs"] = n
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func createDoNothing[T any](tx *gorm.DB, rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	return result.RowsAffected, result.Error
}

func createUpsertAll[T any](tx *gorm.DB, rows []T, columns []clause.Column) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   columns,
		UpdateAll: true,
	}).Create(&rows)
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
