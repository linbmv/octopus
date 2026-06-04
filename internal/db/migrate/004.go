package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 4,
		Up:      migrateGroupsEnabledDefault,
	})
}

// 004: groups.enabled 列由 AutoMigrate 创建后，将存量分组一次性回填为启用。
// 仅在本迁移版本首次执行时运行，后续用户手动禁用的状态不会被覆盖。
func migrateGroupsEnabledDefault(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	// 列尚未创建（理论上 AutoMigrate 已先建好）时直接跳过，避免对缺列的库报错。
	if !db.Migrator().HasTable("groups") || !db.Migrator().HasColumn("groups", "enabled") {
		return nil
	}

	if err := db.Exec("UPDATE groups SET enabled = ?", true).Error; err != nil {
		return fmt.Errorf("failed to backfill groups.enabled: %w", err)
	}
	return nil
}
