package migrate

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// settings 表的列名 "key" 在 MySQL 是保留字，迁移里的裸 SQL 必须按方言引用。
func TestQuoteIdentifierQuotesReservedColumnPerDialect(t *testing.T) {
	if got := quoteIdentifier(mysql.New(mysql.Config{}), "key"); got != "`key`" {
		t.Fatalf("mysql quoted identifier = %q, want %q", got, "`key`")
	}
	if got := quoteIdentifier(postgres.New(postgres.Config{}), "key"); got != `"key"` {
		t.Fatalf("postgres quoted identifier = %q, want %q", got, `"key"`)
	}
	if got := quoteIdentifier(sqlite.Dialector{}, "key"); got == "key" {
		t.Fatalf("sqlite quoted identifier = %q, want quoted form", got)
	}
}

func TestCleanupCompactProbeArtifactsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	if err := db.Exec(`CREATE TABLE settings (key text primary key, value text not null)`).Error; err != nil {
		t.Fatalf("create settings: %v", err)
	}
	if err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, "compact_strategy_probe_enabled", "true").Error; err != nil {
		t.Fatalf("insert setting: %v", err)
	}
	if err := db.Exec(`CREATE TABLE group_items (id integer primary key, compact_strategy text, compact_probe_error text)`).Error; err != nil {
		t.Fatalf("create group_items: %v", err)
	}

	if err := cleanupCompactProbeArtifacts(db); err != nil {
		t.Fatalf("cleanupCompactProbeArtifacts: %v", err)
	}
	if err := cleanupCompactProbeArtifacts(db); err != nil {
		t.Fatalf("cleanupCompactProbeArtifacts second run: %v", err)
	}

	var settingCount int64
	if err := db.Table("settings").Where("key = ?", "compact_strategy_probe_enabled").Count(&settingCount).Error; err != nil {
		t.Fatalf("count setting: %v", err)
	}
	if settingCount != 0 {
		t.Fatalf("compact probe setting count = %d, want 0", settingCount)
	}

	if hasCompactProbeErrorColumn(db) {
		t.Fatal("compact_probe_error column still exists")
	}
}
