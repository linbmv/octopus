package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 6,
		Up:      migrateGroupItemNesting,
	})
}

// 006: group_items 支持 channel/group 两类成员，并迁移唯一索引
func migrateGroupItemNesting(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("group_items") {
		return nil
	}
	if !db.Migrator().HasColumn("group_items", "type") ||
		!db.Migrator().HasColumn("group_items", "target_group_id") {
		return fmt.Errorf("group nesting columns not created by AutoMigrate")
	}

	// 回填存量数据：将所有 type 为空的记录设置为 'channel'
	if err := db.Exec(`
		UPDATE group_items
		SET type = ?
		WHERE type IS NULL OR type = ''
	`, model.GroupItemTypeChannel).Error; err != nil {
		return fmt.Errorf("failed to backfill group_items.type: %w", err)
	}

	// 先创建新索引（事务安全：成功后再删旧索引）
	if !db.Migrator().HasIndex(&model.GroupItem{}, "idx_group_item_unique") {
		if err := db.Migrator().CreateIndex(&model.GroupItem{}, "idx_group_item_unique"); err != nil {
			return fmt.Errorf("failed to create group item unique index: %w", err)
		}
	}

	// 删除旧索引（新索引创建成功后才执行）
	if db.Migrator().HasIndex(&model.GroupItem{}, "idx_group_channel_model") {
		if err := db.Migrator().DropIndex(&model.GroupItem{}, "idx_group_channel_model"); err != nil {
			return fmt.Errorf("failed to drop old group item index: %w", err)
		}
	}

	return nil
}
