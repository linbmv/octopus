package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAddJWTSecretAndTokenVersionUpgradesLegacySQLiteUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id integer PRIMARY KEY AUTOINCREMENT,
		username text,
		password text NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO users (username, password) VALUES (?, ?)",
		"admin", "hash",
	).Error; err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}

	// 生产顺序：AutoMigrate 必须先能为有数据的旧 SQLite 表添加认证列，
	// 然后 after migration 为旧用户生成随机密钥。
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("auto migrate legacy users: %v", err)
	}
	if err := addJWTSecretAndTokenVersion(db); err != nil {
		t.Fatalf("upgrade legacy users: %v", err)
	}

	var gotJWTSecret string
	var gotTokenVersion int
	row := db.Raw("SELECT jwt_secret, token_version FROM users WHERE username = ?", "admin").Row()
	if err := row.Scan(&gotJWTSecret, &gotTokenVersion); err != nil {
		t.Fatalf("load upgraded user: %v", err)
	}
	if gotJWTSecret == "" {
		t.Fatal("jwt_secret was not populated for legacy user")
	}
	if gotTokenVersion != 0 {
		t.Fatalf("token_version = %d, want 0", gotTokenVersion)
	}

	// 迁移必须可重入，便于上次启动在记录迁移状态前中断后安全重试。
	if err := addJWTSecretAndTokenVersion(db); err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
}

func TestAddJWTSecretAndTokenVersionSkipsFreshDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := addJWTSecretAndTokenVersion(db); err != nil {
		t.Fatalf("fresh database migration: %v", err)
	}
	if db.Migrator().HasTable("users") {
		t.Fatal("pre-auto migration unexpectedly created users table")
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("auto migrate fresh users: %v", err)
	}
}
