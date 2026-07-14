package op

import (
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestUserSchemaUpgradeAddsMustChangeColumn 验证在旧 schema（无 must_change_password 列）
// 的数据库上，InitDB 的 AutoMigrate 能幂等地补上新列，保证升级路径不破坏既有部署。
func TestUserSchemaUpgradeAddsMustChangeColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")

	// 用只有旧三列的 users 表建库。
	type oldUser struct {
		ID       uint   `gorm:"primaryKey"`
		Username string `gorm:"unique"`
		Password string `gorm:"not null"`
	}
	g, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	if err := g.Table("users").AutoMigrate(&oldUser{}); err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	if sqlDB, _ := g.DB(); sqlDB != nil {
		_ = sqlDB.Close()
	}

	// 用正式 InitDB 打开：应通过 AutoMigrate 补列。
	if err := db.InitDB("sqlite", path, false); err != nil {
		t.Fatalf("InitDB on old schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if !db.GetDB().Migrator().HasColumn(&model.User{}, "MustChangePassword") {
		t.Fatal("AutoMigrate did not add must_change_password column on upgrade")
	}
}
