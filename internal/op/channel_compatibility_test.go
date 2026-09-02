package op

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestChannelCreateAndUpdateCompatibilityValues(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "channels.db"), false); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	channel := &model.Channel{
		Name:     "compat-channel",
		Type:     model.ChannelProviderOpenAI,
		BaseUrls: []model.BaseUrl{{URL: "https://one.example", Delay: 0}, {URL: "https://two.example", Delay: 5}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "key-one"}, {Enabled: false, ChannelKey: "key-two"}},
		Models:   []model.ChannelModel{{Name: "gpt-compat"}},
	}
	if err := ChannelCreate(channel, context.Background()); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().Preload("Keys").First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	if stored.BaseURL != "https://one.example" || stored.Key != "key-one" || len(stored.BaseUrls) != 2 || len(stored.Keys) != 2 {
		t.Fatalf("stored compatibility values = %#v", stored)
	}
	enabled := true
	remark := "primary"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:           channel.ID,
		BaseUrls:     &[]model.BaseUrl{{URL: "https://updated.example"}},
		KeysToUpdate: []model.ChannelKeyUpdateRequest{{ID: stored.Keys[0].ID, Enabled: &enabled, Remark: &remark}},
	}, context.Background()); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	var updated model.Channel
	if err := db.GetDB().Preload("Keys").First(&updated, channel.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if updated.BaseURL != "https://updated.example" || len(updated.BaseUrls) != 1 || updated.Keys[0].Remark != "primary" {
		t.Fatalf("updated compatibility values = %#v", updated)
	}
}
