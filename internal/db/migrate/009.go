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

	if db.Migrator().HasTable("group_items") && hasCompactProbeErrorColumn(db) {
		if err := dropCompactProbeErrorColumn(db); err != nil {
			return fmt.Errorf("failed to drop group_items.compact_probe_error: %w", err)
		}
	}

	return nil
}

func hasCompactProbeErrorColumn(db *gorm.DB) bool {
	switch db.Dialector.Name() {
	case "sqlite":
		var name string
		db.Raw("SELECT name FROM pragma_table_info(?) WHERE name = ? LIMIT 1", "group_items", "compact_probe_error").Scan(&name)
		return name == "compact_probe_error"
	case "mysql":
		var count int64
		db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", "group_items", "compact_probe_error").Scan(&count)
		return count > 0
	case "postgres":
		var count int64
		db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?", "group_items", "compact_probe_error").Scan(&count)
		return count > 0
	default:
		return db.Migrator().HasColumn("group_items", "compact_probe_error")
	}
}

func dropCompactProbeErrorColumn(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite":
		return db.Exec("ALTER TABLE group_items DROP COLUMN compact_probe_error").Error
	case "mysql":
		return db.Exec("ALTER TABLE `group_items` DROP COLUMN `compact_probe_error`").Error
	case "postgres":
		return db.Exec(`ALTER TABLE "group_items" DROP COLUMN IF EXISTS "compact_probe_error"`).Error
	default:
		return db.Exec("ALTER TABLE group_items DROP COLUMN compact_probe_error").Error
	}
}
