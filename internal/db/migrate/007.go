package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 7,
		Up:      migrateCodexOAuthColumns,
	})
}

// 007: verify codex oauth columns are created by AutoMigrate.
func migrateCodexOAuthColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channel_keys") {
		return nil
	}

	requiredColumns := []string{
		"codex_access_token",
		"codex_refresh_token",
		"codex_id_token",
		"codex_token_expiry",
		"codex_account_id",
		"codex_plan_type",
		"codex_email",
	}
	for _, column := range requiredColumns {
		if !db.Migrator().HasColumn("channel_keys", column) {
			return fmt.Errorf("codex oauth columns not created by AutoMigrate")
		}
	}
	return nil
}
