package migrate

import (
	"encoding/json"
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

const retiredCodexChannelType = "openai/codex"

func init() {
	RegisterAfterAutoMigration(Migration{Version: 17, Up: purgeRetiredOAuthAndSelfHealing})
}

// purgeRetiredOAuthAndSelfHealing is intentionally destructive. The retired
// features were explicitly removed, so this migration clears their persisted
// data instead of leaving an inert compatibility surface behind.
func purgeRetiredOAuthAndSelfHealing(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := purgeRetiredCodexChannels(tx); err != nil {
			return err
		}
		for _, table := range []string{
			"diagnostic_attempts",
			"channel_patches",
			"diagnostic_sessions",
			"channel_baselines",
		} {
			if err := dropRetiredTable(tx, table); err != nil {
				return err
			}
		}
		if err := dropRetiredChannelColumn(tx); err != nil {
			return err
		}
		return nil
	})
}

func purgeRetiredCodexChannels(db *gorm.DB) error {
	if !db.Migrator().HasTable("channels") || !db.Migrator().HasColumn("channels", "type") {
		return nil
	}

	var channelIDs []int
	if err := db.Table("channels").Where("type = ?", retiredCodexChannelType).Pluck("id", &channelIDs).Error; err != nil {
		return fmt.Errorf("find retired Codex channels: %w", err)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	var keyIDs []int
	if db.Migrator().HasTable("channel_keys") {
		if err := db.Table("channel_keys").Where("channel_id IN ?", channelIDs).Pluck("id", &keyIDs).Error; err != nil {
			return fmt.Errorf("find retired Codex channel keys: %w", err)
		}
	}
	if err := purgeRetiredCodexRelayLogs(db, channelIDs); err != nil {
		return err
	}

	if err := deleteByChannelIDs(db, "group_items", channelIDs); err != nil {
		return err
	}
	if err := deleteByChannelIDs(db, "capability_evidences", channelIDs); err != nil {
		return err
	}
	if err := deleteByChannelIDs(db, "stats_channels", channelIDs); err != nil {
		return err
	}
	if db.Migrator().HasTable("stats_channel_keys") {
		query := db.Table("stats_channel_keys").Where("channel_id IN ?", channelIDs)
		if len(keyIDs) > 0 {
			query = query.Or("channel_key_id IN ?", keyIDs)
		}
		if err := query.Delete(nil).Error; err != nil {
			return fmt.Errorf("delete retired Codex channel key stats: %w", err)
		}
	}
	if err := deleteByChannelIDs(db, "channel_keys", channelIDs); err != nil {
		return err
	}
	if err := db.Table("channels").Where("id IN ?", channelIDs).Delete(nil).Error; err != nil {
		return fmt.Errorf("delete retired Codex channels: %w", err)
	}
	return nil
}

func purgeRetiredCodexRelayLogs(db *gorm.DB, channelIDs []int) error {
	if !db.Migrator().HasTable("relay_logs") {
		return nil
	}
	retired := make(map[int]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		retired[id] = struct{}{}
	}

	rows, err := db.Table("relay_logs").Select("id, channel_id, attempts").Rows()
	if err != nil {
		return fmt.Errorf("scan relay logs for retired Codex attempts: %w", err)
	}
	type relayLogUpdate struct {
		id       int64
		attempts []model.ChannelAttempt
	}
	var deleteIDs []int64
	var updates []relayLogUpdate
	for rows.Next() {
		var row model.RelayLog
		if err := db.ScanRows(rows, &row); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read relay log for retired Codex cleanup: %w", err)
		}
		if _, ok := retired[row.ChannelId]; ok {
			deleteIDs = append(deleteIDs, row.ID)
			continue
		}

		filtered := make([]model.ChannelAttempt, 0, len(row.Attempts))
		removed := false
		for _, attempt := range row.Attempts {
			if _, ok := retired[attempt.ChannelID]; ok {
				removed = true
				continue
			}
			filtered = append(filtered, attempt)
		}
		if !removed {
			continue
		}
		updates = append(updates, relayLogUpdate{id: row.ID, attempts: filtered})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate relay logs for retired Codex cleanup: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close relay logs for retired Codex cleanup: %w", err)
	}
	for _, id := range deleteIDs {
		if err := db.Exec("DELETE FROM relay_logs WHERE id = ?", id).Error; err != nil {
			return fmt.Errorf("delete retired Codex relay log %d: %w", id, err)
		}
	}
	for _, update := range updates {
		payload, err := json.Marshal(update.attempts)
		if err != nil {
			return fmt.Errorf("encode cleaned relay log %d attempts: %w", update.id, err)
		}
		if err := db.Exec("UPDATE relay_logs SET attempts = ? WHERE id = ?", string(payload), update.id).Error; err != nil {
			return fmt.Errorf("update cleaned relay log %d: %w", update.id, err)
		}
	}
	return nil
}

func deleteByChannelIDs(db *gorm.DB, table string, channelIDs []int) error {
	if !db.Migrator().HasTable(table) {
		return nil
	}
	if err := db.Exec("DELETE FROM "+table+" WHERE channel_id IN ?", channelIDs).Error; err != nil {
		return fmt.Errorf("delete retired Codex rows from %s: %w", table, err)
	}
	return nil
}

func dropRetiredTable(db *gorm.DB, table string) error {
	if !db.Migrator().HasTable(table) {
		return nil
	}
	if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
		return fmt.Errorf("drop retired table %s: %w", table, err)
	}
	return nil
}

func dropRetiredChannelColumn(db *gorm.DB) error {
	if !db.Migrator().HasTable("channels") || !db.Migrator().HasColumn("channels", "self_healing_enabled") {
		return nil
	}
	statement := "ALTER TABLE channels DROP COLUMN self_healing_enabled"
	if db.Name() == "postgres" {
		statement = "ALTER TABLE channels DROP COLUMN IF EXISTS self_healing_enabled"
	}
	if err := db.Exec(statement).Error; err != nil {
		return fmt.Errorf("drop retired channels.self_healing_enabled column: %w", err)
	}
	return nil
}
