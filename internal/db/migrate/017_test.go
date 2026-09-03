package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPurgeRetiredOAuthAndSelfHealing(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := database.AutoMigrate(
		&model.Channel{},
		&model.ChannelKey{},
		&model.Group{},
		&model.GroupItem{},
		&model.CapabilityEvidence{},
		&model.StatsChannel{},
		&model.StatsChannelKey{},
		&model.RelayLog{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	if err := database.Exec("ALTER TABLE channels ADD COLUMN self_healing_enabled BOOLEAN NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatalf("add retired channel column: %v", err)
	}

	oldChannel := model.Channel{ID: 1, Name: "legacy-codex", Type: "openai/codex", BaseUrls: []model.BaseUrl{{URL: "https://legacy.example"}}}
	activeChannel := model.Channel{ID: 2, Name: "active", Type: "openai/chat_completions", BaseUrls: []model.BaseUrl{{URL: "https://active.example"}}}
	for _, channel := range []*model.Channel{&oldChannel, &activeChannel} {
		if err := database.Create(channel).Error; err != nil {
			t.Fatalf("create channel %d: %v", channel.ID, err)
		}
	}
	if err := database.Exec("UPDATE channels SET self_healing_enabled = 1 WHERE id = 1").Error; err != nil {
		t.Fatalf("set retired channel column: %v", err)
	}
	oldKey := model.ChannelKey{ID: 11, ChannelID: 1, ChannelKey: "retired-oauth-json"}
	activeKey := model.ChannelKey{ID: 12, ChannelID: 2, ChannelKey: "active-key"}
	for _, key := range []*model.ChannelKey{&oldKey, &activeKey} {
		if err := database.Create(key).Error; err != nil {
			t.Fatalf("create channel key %d: %v", key.ID, err)
		}
	}
	if err := database.Create(&model.Group{ID: 21, Name: "group", Mode: model.GroupModeFailover}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := database.Create(&model.GroupItem{ID: 31, GroupID: 21, Type: model.GroupItemTypeChannel, ChannelID: 1, ModelName: "legacy"}).Error; err != nil {
		t.Fatalf("create retired group item: %v", err)
	}
	if err := database.Create(&model.GroupItem{ID: 32, GroupID: 21, Type: model.GroupItemTypeChannel, ChannelID: 2, ModelName: "active"}).Error; err != nil {
		t.Fatalf("create active group item: %v", err)
	}
	if err := database.Create(&model.StatsChannel{ChannelID: 1}).Error; err != nil {
		t.Fatalf("create retired channel stats: %v", err)
	}
	if err := database.Create(&model.StatsChannelKey{ChannelID: 1, ChannelKeyID: 11}).Error; err != nil {
		t.Fatalf("create retired channel key stats: %v", err)
	}
	if err := database.Create(&model.CapabilityEvidence{
		ID: 41, ChannelID: 1, ChannelKeyID: 11, Model: "legacy", WireProtocol: "openai/responses",
		Capability: model.CapabilityText, Endpoint: "https://legacy.example", EndpointFingerprint: "legacy-endpoint",
		Status: model.CapabilitySupported, ScopeFingerprint: "legacy-scope", Source: "probe",
	}).Error; err != nil {
		t.Fatalf("create retired capability evidence: %v", err)
	}
	if err := database.Create(&model.RelayLog{
		ID: 51, ChannelId: 1, Attempts: []model.ChannelAttempt{{ChannelID: 1, ChannelKeyID: 11, ChannelName: "legacy"}},
	}).Error; err != nil {
		t.Fatalf("create retired relay log: %v", err)
	}
	if err := database.Create(&model.RelayLog{
		ID: 52, ChannelId: 2, Attempts: []model.ChannelAttempt{
			{ChannelID: 1, ChannelKeyID: 11, ChannelName: "legacy"},
			{ChannelID: 2, ChannelKeyID: 12, ChannelName: "active"},
		},
	}).Error; err != nil {
		t.Fatalf("create mixed relay log: %v", err)
	}
	for _, table := range []string{"diagnostic_attempts", "channel_patches", "diagnostic_sessions", "channel_baselines"} {
		if err := database.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)").Error; err != nil {
			t.Fatalf("create retired table %s: %v", table, err)
		}
	}

	if err := purgeRetiredOAuthAndSelfHealing(database); err != nil {
		t.Fatalf("purge retired features: %v", err)
	}

	var channelCount int64
	if err := database.Model(&model.Channel{}).Where("id = 1").Count(&channelCount).Error; err != nil {
		t.Fatalf("count retired channel: %v", err)
	}
	if channelCount != 0 {
		t.Fatal("retired Codex channel was not deleted")
	}
	if err := database.Model(&model.Channel{}).Where("id = 2").Count(&channelCount).Error; err != nil {
		t.Fatalf("count active channel: %v", err)
	}
	if channelCount != 1 {
		t.Fatal("active channel was deleted")
	}
	for _, check := range []struct {
		name  string
		model any
		where string
	}{
		{name: "channel key", model: &model.ChannelKey{}, where: "id = 11"},
		{name: "group item", model: &model.GroupItem{}, where: "id = 31"},
		{name: "channel stats", model: &model.StatsChannel{}, where: "channel_id = 1"},
		{name: "channel key stats", model: &model.StatsChannelKey{}, where: "channel_key_id = 11"},
		{name: "capability evidence", model: &model.CapabilityEvidence{}, where: "id = 41"},
	} {
		var count int64
		if err := database.Model(check.model).Where(check.where).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("retired %s was not deleted", check.name)
		}
	}
	var activeItemCount int64
	if err := database.Model(&model.GroupItem{}).Where("id = 32").Count(&activeItemCount).Error; err != nil {
		t.Fatalf("count active group item: %v", err)
	}
	if activeItemCount != 1 {
		t.Fatal("active group item was deleted")
	}

	var relayCount int64
	if err := database.Model(&model.RelayLog{}).Count(&relayCount).Error; err != nil {
		t.Fatalf("count relay logs: %v", err)
	}
	if relayCount != 1 {
		t.Fatalf("relay log count = %d, want one mixed log", relayCount)
	}
	var mixed model.RelayLog
	if err := database.First(&mixed, 52).Error; err != nil {
		t.Fatalf("load mixed relay log: %v", err)
	}
	if len(mixed.Attempts) != 1 || mixed.Attempts[0].ChannelID != 2 {
		t.Fatalf("mixed relay log attempts = %+v, want only active attempt", mixed.Attempts)
	}
	for _, table := range []string{"diagnostic_attempts", "channel_patches", "diagnostic_sessions", "channel_baselines"} {
		if database.Migrator().HasTable(table) {
			t.Errorf("retired table %s still exists", table)
		}
	}
	if database.Migrator().HasColumn(&model.Channel{}, "self_healing_enabled") {
		t.Fatal("retired channels.self_healing_enabled column still exists")
	}
}
