package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 11,
		Up:      migrateNestedGroupsAndTemporaryToggles,
	})
}

// migrateNestedGroupsAndTemporaryToggles 回填新开关与成员类型。
// AutoMigrate 负责增加列和外键；本迁移只在首次升级时处理存量记录，
// 因而不会在后续启动时覆盖用户主动设置的禁用状态。
func migrateNestedGroupsAndTemporaryToggles(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if db.Migrator().HasTable("groups") {
		if !db.Migrator().HasColumn("groups", "enabled") {
			return fmt.Errorf("groups.enabled not created by AutoMigrate")
		}
		if err := db.Table("groups").Where("enabled = ?", false).Update("enabled", true).Error; err != nil {
			return fmt.Errorf("failed to backfill groups.enabled: %w", err)
		}
	}
	if db.Migrator().HasTable("group_items") {
		for _, column := range []string{"type", "target_group_id", "disabled"} {
			if !db.Migrator().HasColumn("group_items", column) {
				return fmt.Errorf("group_items.%s not created by AutoMigrate", column)
			}
		}
		if err := db.Table("group_items").
			Where("type IS NULL OR type = ''").
			Update("type", model.GroupItemTypeChannelModel).Error; err != nil {
			return fmt.Errorf("failed to backfill group_items.type: %w", err)
		}
	}
	return nil
}
