package migrate

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDropOrphanStatsModelsTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	if err := db.Exec(`CREATE TABLE stats_models (id integer primary key, name text, channel_id integer)`).Error; err != nil {
		t.Fatalf("create stats_models: %v", err)
	}

	if err := dropOrphanStatsModelsTable(db); err != nil {
		t.Fatalf("dropOrphanStatsModelsTable: %v", err)
	}
	if db.Migrator().HasTable("stats_models") {
		t.Fatal("stats_models table still exists after drop")
	}

	// 幂等：表不存在时再次运行不应报错。
	if err := dropOrphanStatsModelsTable(db); err != nil {
		t.Fatalf("dropOrphanStatsModelsTable second run: %v", err)
	}
}
