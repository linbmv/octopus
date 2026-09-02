package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 9,
		Up:      migrateDropLegacyChannelSchema,
	})
}

// migrateDropLegacyChannelSchema 删除遗留的渠道多地址字段和转发日志表。
func migrateDropLegacyChannelSchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if db.Migrator().HasTable("channels") {
		// The current upstream-compatible schema also has a channels.base_urls
		// JSON column, but its channel_keys table is the discriminator. Only
		// remove the old Edge column when the legacy multi-key table is absent;
		// the startup schema guard rejects an actual Edge database before this
		// migration runs.
		if db.Migrator().HasTable("channel_keys") {
			return dropRelayLogsOnly(db)
		}
		if err := dropColumnIfExists(db, &model.Channel{}, "channels", "base_urls"); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable("relay_logs") {
		if err := db.Migrator().DropTable("relay_logs"); err != nil {
			return fmt.Errorf("failed to drop relay_logs: %w", err)
		}
	}
	return nil
}

func dropRelayLogsOnly(db *gorm.DB) error {
	if db.Migrator().HasTable("relay_logs") {
		if err := db.Migrator().DropTable("relay_logs"); err != nil {
			return fmt.Errorf("failed to drop relay_logs: %w", err)
		}
	}
	return nil
}
