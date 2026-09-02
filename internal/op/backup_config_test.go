package op

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestConfigBackupRoundTripExcludesStatistics(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "source.db"), false); err != nil {
		t.Fatalf("initialize source database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	channel := model.Channel{
		ID:      10,
		Name:    "source-channel",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://example.test/v1",
		Key:     "channel-secret",
		Models: []model.ChannelModel{{
			ID:        20,
			ChannelID: 10,
			Name:      "gpt-test",
			Source:    model.ChannelModelSourceManual,
		}},
		StatsMetrics: model.StatsMetrics{InputToken: 999, RequestSuccess: 7},
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	group := model.Group{ID: 30, Name: "source-group", Mode: model.GroupModeManual}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatalf("create source group: %v", err)
	}
	item := model.GroupItem{ID: 40, GroupID: group.ID, Type: model.GroupItemTypeChannelModel, ChannelModelID: &channel.Models[0].ID, Priority: 1}
	if err := db.GetDB().Create(&item).Error; err != nil {
		t.Fatalf("create source group item: %v", err)
	}

	dump, err := DBExportConfig(context.Background())
	if err != nil {
		t.Fatalf("export config: %v", err)
	}
	if dump.Version != configDumpVersion || dump.Scope != model.ConfigDumpScope || len(dump.Channels) != 1 || len(dump.ChannelModels) != 1 || len(dump.Groups) != 1 || len(dump.GroupItems) != 1 {
		t.Fatalf("unexpected config dump: %#v", dump)
	}
	if dump.Channels[0].Key != "channel-secret" || dump.ChannelModels[0].Name != "gpt-test" {
		t.Fatalf("configuration was not exported: %#v", dump)
	}
	if len(dump.Channels[0].BaseUrls) != 1 || len(dump.Channels[0].Keys) != 1 || dump.Channels[0].Keys[0].ChannelKey != "channel-secret" {
		t.Fatalf("multi-value configuration was not exported: %#v", dump.Channels[0])
	}
	encrypted, err := DBExportConfigEncrypted(context.Background(), []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("export encrypted config: %v", err)
	}
	decoded, err := DecodeConfigDump(encrypted, []byte("correct horse battery staple"))
	if err != nil || len(decoded.Channels) != 1 || decoded.Channels[0].Key != "channel-secret" {
		t.Fatalf("encrypted config round trip failed: dump=%#v err=%v", decoded, err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "target.db"), false); err != nil {
		t.Fatalf("initialize target database: %v", err)
	}
	result, err := DBImportConfig(context.Background(), dump)
	if err != nil {
		t.Fatalf("import config: %v", err)
	}
	if result.RowsAffected["channels"] != 1 || result.RowsAffected["channel_keys"] != 1 || result.RowsAffected["channel_models"] != 1 || result.RowsAffected["groups"] != 1 || result.RowsAffected["group_items"] != 1 {
		t.Fatalf("unexpected import result: %#v", result.RowsAffected)
	}
	var restored model.Channel
	if err := db.GetDB().First(&restored, 10).Error; err != nil {
		t.Fatalf("load restored channel: %v", err)
	}
	if restored.Key != "channel-secret" || restored.InputToken != 0 || restored.RequestSuccess != 0 {
		t.Fatalf("runtime statistics leaked into config restore: %#v", restored)
	}
	var restoredKeys []model.ChannelKey
	if err := db.GetDB().Where("channel_id = ?", 10).Find(&restoredKeys).Error; err != nil {
		t.Fatalf("load restored keys: %v", err)
	}
	if len(restoredKeys) != 1 || restoredKeys[0].ChannelKey != "channel-secret" {
		t.Fatalf("restored keys = %#v", restoredKeys)
	}
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", 10).
		Updates(map[string]any{"input_token": 1234, "request_success": 12}).Error; err != nil {
		t.Fatalf("seed target statistics: %v", err)
	}
	if _, err := DBImportConfig(context.Background(), dump); err != nil {
		t.Fatalf("re-import config: %v", err)
	}
	if err := db.GetDB().First(&restored, 10).Error; err != nil {
		t.Fatalf("reload restored channel: %v", err)
	}
	if restored.InputToken != 1234 || restored.RequestSuccess != 12 {
		t.Fatalf("config import overwrote runtime statistics: %#v", restored)
	}
}
