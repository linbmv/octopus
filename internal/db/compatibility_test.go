package db

import (
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestInitDBRestartPreservesMultiValueCompatibilitySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "octopus.db")
	if err := InitDB("sqlite", path, false); err != nil {
		t.Fatalf("initial database: %v", err)
	}
	if !GetDB().Migrator().HasColumn("channels", "base_urls") || !GetDB().Migrator().HasTable(&model.ChannelKey{}) {
		t.Fatal("initial database did not create compatibility fields")
	}
	if err := GetDB().Create(&model.Channel{Name: "restart", BaseURL: "https://example.test", Key: "key"}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := Close(); err != nil {
		t.Fatalf("close initial database: %v", err)
	}
	if err := InitDB("sqlite", path, false); err != nil {
		t.Fatalf("restart database: %v", err)
	}
	t.Cleanup(func() { _ = Close() })
	if !GetDB().Migrator().HasColumn("channels", "base_urls") || !GetDB().Migrator().HasTable(&model.ChannelKey{}) {
		t.Fatal("restart lost compatibility fields")
	}
}
