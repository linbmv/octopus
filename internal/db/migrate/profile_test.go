package migrate

import (
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openProfileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func TestDetectSchemaProfile(t *testing.T) {
	t.Run("unknown without channels", func(t *testing.T) {
		if got := DetectSchemaProfile(openProfileTestDB(t)); got != SchemaProfileUnknown {
			t.Fatalf("profile = %q, want %q", got, SchemaProfileUnknown)
		}
	})

	t.Run("upstream markers", func(t *testing.T) {
		db := openProfileTestDB(t)
		if err := db.Exec("CREATE TABLE channels (id integer primary key)").Error; err != nil {
			t.Fatal(err)
		}
		if got := DetectSchemaProfile(db); got != SchemaProfileUpstream {
			t.Fatalf("profile = %q, want %q", got, SchemaProfileUpstream)
		}
	})

	t.Run("edge multi key marker", func(t *testing.T) {
		db := openProfileTestDB(t)
		if err := db.Exec("CREATE TABLE channels (id integer primary key, base_urls text)").Error; err != nil {
			t.Fatal(err)
		}
		if got := DetectSchemaProfile(db); got != SchemaProfileLegacyEdge {
			t.Fatalf("profile = %q, want %q", got, SchemaProfileLegacyEdge)
		}
	})

	t.Run("additive upstream multi key schema", func(t *testing.T) {
		db := openProfileTestDB(t)
		if err := db.Exec("CREATE TABLE channels (id integer primary key, base_url text, key text, base_urls text)").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("CREATE TABLE channel_keys (id integer primary key, channel_id integer, channel_key text)").Error; err != nil {
			t.Fatal(err)
		}
		if got := DetectSchemaProfile(db); got != SchemaProfileUpstream {
			t.Fatalf("profile = %q, want %q", got, SchemaProfileUpstream)
		}
	})
}

func TestEnsureUpstreamSchemaCompatibleRejectsLegacyEdge(t *testing.T) {
	db := openProfileTestDB(t)
	if err := db.Exec("CREATE TABLE channels (id integer primary key)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE channel_keys (id integer primary key, channel_id integer, channel_key text)").Error; err != nil {
		t.Fatal(err)
	}
	err := EnsureUpstreamSchemaCompatible(db)
	if err == nil || !strings.Contains(err.Error(), "legacy Edge database schema detected") {
		t.Fatalf("error = %v, want legacy Edge refusal", err)
	}
}

func TestMigrateChannelCompatibilityFieldsIsAdditive(t *testing.T) {
	db := openProfileTestDB(t)
	if err := db.AutoMigrate(&model.Channel{}, &model.ChannelKey{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	channel := model.Channel{ID: 10, Name: "legacy-upstream", BaseURL: "https://example.test", Key: "key-1"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := migrateChannelCompatibilityFields(db); err != nil {
		t.Fatalf("compatibility migration: %v", err)
	}
	var restored model.Channel
	if err := db.First(&restored, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(restored.BaseUrls) != 1 || restored.BaseUrls[0].URL != channel.BaseURL {
		t.Fatalf("base URLs = %#v", restored.BaseUrls)
	}
	var keys []model.ChannelKey
	if err := db.Where("channel_id = ?", channel.ID).Find(&keys).Error; err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ChannelKey != channel.Key || !keys[0].Enabled {
		t.Fatalf("keys = %#v", keys)
	}
	if err := migrateChannelCompatibilityFields(db); err != nil {
		t.Fatalf("rerun compatibility migration: %v", err)
	}
	if err := db.Model(&model.ChannelKey{}).Where("channel_id = ?", channel.ID).Count(new(int64)).Error; err != nil {
		t.Fatal(err)
	}
}
