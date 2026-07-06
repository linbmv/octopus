package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 9,
		Up:      cleanupCompactProbeArtifacts,
	})
}

func cleanupCompactProbeArtifacts(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	if db.Migrator().HasTable("settings") {
		if err := db.Exec("DELETE FROM settings WHERE key = ?", "compact_strategy_probe_enabled").Error; err != nil {
			return fmt.Errorf("failed to delete compact probe setting: %w", err)
		}
	}

	if db.Migrator().HasTable("group_items") && db.Migrator().HasColumn("group_items", "compact_probe_error") {
		if err := db.Migrator().DropColumn("group_items", "compact_probe_error"); err != nil {
			return fmt.Errorf("failed to drop group_items.compact_probe_error: %w", err)
		}
	}

	return nil
}
