package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 7,
		Up:      addChannelLimitColumns,
	})
}

// 007: add per-channel upstream RPM and concurrency limits.
func addChannelLimitColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	columns := map[string]string{
		"rpm_limit":       "INTEGER NOT NULL DEFAULT 0",
		"max_concurrency": "INTEGER NOT NULL DEFAULT 0",
	}
	for name, typ := range columns {
		if db.Migrator().HasColumn("channels", name) {
			continue
		}
		if err := db.Exec(fmt.Sprintf("ALTER TABLE channels ADD COLUMN %s %s", name, typ)).Error; err != nil {
			return fmt.Errorf("failed to add channels.%s: %w", name, err)
		}
	}
	return nil
}
