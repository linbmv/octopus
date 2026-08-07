package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBackfillChannelKeyStatsFromRelayAttempts(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := database.AutoMigrate(&model.Channel{}, &model.ChannelKey{}, &model.RelayLog{}, &model.StatsChannelKey{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	for _, key := range []model.ChannelKey{
		{ID: 10, ChannelID: 1, ChannelKey: "key-10"},
		{ID: 11, ChannelID: 1, ChannelKey: "key-11"},
		{ID: 12, ChannelID: 2, ChannelKey: "key-12"},
	} {
		if err := database.Create(&key).Error; err != nil {
			t.Fatalf("create key %d: %v", key.ID, err)
		}
	}
	logs := []model.RelayLog{
		{ID: 1, Attempts: []model.ChannelAttempt{
			{ChannelID: 1, ChannelKeyID: 10, Status: model.AttemptSuccess, Duration: 25},
			{ChannelID: 1, ChannelKeyID: 11, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelKey, Duration: 30},
			{ChannelID: 1, ChannelKeyID: 11, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelClient, Duration: 99},
		}},
		{ID: 2, Attempts: []model.ChannelAttempt{
			{ChannelID: 2, ChannelKeyID: 12, Status: model.AttemptSuccess, Duration: 40},
			{ChannelID: 999, ChannelKeyID: 12, Status: model.AttemptSuccess, Duration: 100},
			{ChannelID: 1, ChannelKeyID: 999, Status: model.AttemptSuccess, Duration: 100},
		}},
	}
	for _, log := range logs {
		if err := database.Create(&log).Error; err != nil {
			t.Fatalf("create relay log %d: %v", log.ID, err)
		}
	}

	if err := backfillChannelKeyStats(database); err != nil {
		t.Fatalf("backfill channel key stats: %v", err)
	}
	var got10, got11, got12 model.StatsChannelKey
	if err := database.First(&got10, 10).Error; err != nil {
		t.Fatalf("load channel key 10 stats: %v", err)
	}
	if err := database.First(&got11, 11).Error; err != nil {
		t.Fatalf("load channel key 11 stats: %v", err)
	}
	if err := database.First(&got12, 12).Error; err != nil {
		t.Fatalf("load channel key 12 stats: %v", err)
	}
	if got10.ChannelID != 1 || got10.RequestSuccess != 1 || got10.RequestFailed != 0 || got10.WaitTime != 25 {
		t.Fatalf("key 10 stats = %#v", got10)
	}
	if got11.ChannelID != 1 || got11.RequestSuccess != 0 || got11.RequestFailed != 1 || got11.WaitTime != 30 {
		t.Fatalf("key 11 stats = %#v", got11)
	}
	if got12.ChannelID != 2 || got12.RequestSuccess != 1 || got12.RequestFailed != 0 || got12.WaitTime != 40 {
		t.Fatalf("key 12 stats = %#v", got12)
	}

	// The migration is safe to retry before its migration record is committed:
	// upserted values are reconstructed, not incremented a second time.
	if err := backfillChannelKeyStats(database); err != nil {
		t.Fatalf("idempotent backfill: %v", err)
	}
	var count int64
	if err := database.Model(&model.StatsChannelKey{}).Count(&count).Error; err != nil {
		t.Fatalf("count channel key stats: %v", err)
	}
	if count != 3 {
		t.Fatalf("channel key stats count = %d, want 3", count)
	}
}

func TestBackfillChannelKeyStatsSkipsMissingRelayLogTables(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := database.AutoMigrate(&model.StatsChannelKey{}, &model.ChannelKey{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	if err := backfillChannelKeyStats(database); err == nil {
		t.Fatal("backfill unexpectedly succeeded without relay_logs table")
	}
}
