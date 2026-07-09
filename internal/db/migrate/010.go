package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 10,
		Up:      dropOrphanStatsModelsTable,
	})
}

// dropOrphanStatsModelsTable 删除 stats_models 表：模型维度统计从未接入写入链路，
// 表在所有部署中恒为空，作为孤儿功能随代码一并移除（2026-07-09 复审 W-2v2）。
func dropOrphanStatsModelsTable(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("stats_models") {
		return nil
	}
	if err := db.Migrator().DropTable("stats_models"); err != nil {
		return fmt.Errorf("failed to drop stats_models table: %w", err)
	}
	return nil
}
