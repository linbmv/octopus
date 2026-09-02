package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 6,
		Up:      migrateDropLegacyGroupColumns,
	})
}

// migrateDropLegacyGroupColumns 移除旧版分组模式字段，这些字段已不再使用。
// 旧版 groups 表包含 match_regex、first_token_time_out、session_keep_time，
// 这些字段已不再使用；mode 是当前分组模型的有效字段，不属于清理范围。
// group_items.weight 现在是加权路由配置的一部分，必须保留；旧 Edge 数据库
// 会在启动前被 schema profile guard 拒绝，不在这里做破坏性清理。
func migrateDropLegacyGroupColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	if db.Migrator().HasTable("groups") {
		legacyGroupColumns := []string{"match_regex", "first_token_time_out", "session_keep_time"}
		for _, column := range legacyGroupColumns {
			if err := dropColumnIfExists(db, &model.Group{}, "groups", column); err != nil {
				return err
			}
		}
	}

	return nil
}
