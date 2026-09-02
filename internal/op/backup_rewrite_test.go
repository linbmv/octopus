package op

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func strPtr(s string) *string { return &s }

// 改写规则必须能跨部署迁移，否则导入后渠道行为与源实例不一致。
func TestConfigBackupRoundTripPreservesRewriteRules(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "source.db"), false); err != nil {
		t.Fatalf("initialize source database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	channel := model.Channel{
		ID:      10,
		Name:    "rewrite-channel",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://example.test/v1",
		Key:     "channel-secret",
		HeaderRules: []model.HeaderRule{
			{Action: "set", HeaderKey: "X-Trace", HeaderValue: "on"},
			{Action: "remove", HeaderKey: "X-Debug"},
		},
		JSONRewriteRules: []model.JSONRewriteRule{
			{Action: "override", Path: "/tools/0/type", Value: strPtr(`"custom"`)},
			{Action: "remove", Path: "/stream"},
		},
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create source channel: %v", err)
	}

	dump, err := DBExportConfig(context.Background())
	if err != nil {
		t.Fatalf("export config: %v", err)
	}
	if len(dump.Channels) != 1 {
		t.Fatalf("unexpected channel count: %#v", dump.Channels)
	}
	exported := dump.Channels[0]
	if len(exported.HeaderRules) != 2 || exported.HeaderRules[0].HeaderValue != "on" {
		t.Fatalf("header rules were not exported: %#v", exported.HeaderRules)
	}
	if len(exported.JSONRewriteRules) != 2 || exported.JSONRewriteRules[0].Path != "/tools/0/type" {
		t.Fatalf("json rewrite rules were not exported: %#v", exported.JSONRewriteRules)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "target.db"), false); err != nil {
		t.Fatalf("initialize target database: %v", err)
	}
	if _, err := DBImportConfig(context.Background(), dump); err != nil {
		t.Fatalf("import config: %v", err)
	}

	var restored model.Channel
	if err := db.GetDB().First(&restored, 10).Error; err != nil {
		t.Fatalf("load imported channel: %v", err)
	}
	if len(restored.HeaderRules) != 2 || restored.HeaderRules[1].Action != "remove" {
		t.Fatalf("header rules were not imported: %#v", restored.HeaderRules)
	}
	if len(restored.JSONRewriteRules) != 2 || restored.JSONRewriteRules[1].Path != "/stream" {
		t.Fatalf("json rewrite rules were not imported: %#v", restored.JSONRewriteRules)
	}
	if restored.JSONRewriteRules[0].Value == nil || *restored.JSONRewriteRules[0].Value != `"custom"` {
		t.Fatalf("override value was not imported: %#v", restored.JSONRewriteRules[0])
	}
}

// 导入文件不可信：非法规则必须被丢弃并告警，不能绕过实时编辑的校验。
func TestConfigImportDropsInvalidRewriteRules(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "target.db"), false); err != nil {
		t.Fatalf("initialize target database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	dump := &model.ConfigDump{
		Version: configDumpVersion,
		Scope:   model.ConfigDumpScope,
		Channels: []model.ConfigChannel{{
			ID:      11,
			Name:    "hostile-import",
			Type:    model.ChannelProviderOpenAI,
			Enabled: true,
			BaseURL: "https://example.test/v1",
			Key:     "channel-secret",
			// 凭据头改写与缺少 value 的 override 都应被拒绝。
			HeaderRules:      []model.HeaderRule{{Action: "set", HeaderKey: "Authorization", HeaderValue: "Bearer attacker"}},
			JSONRewriteRules: []model.JSONRewriteRule{{Action: "override", Path: "/model"}},
		}},
	}
	result, err := DBImportConfig(context.Background(), dump)
	if err != nil {
		t.Fatalf("import config: %v", err)
	}

	var restored model.Channel
	if err := db.GetDB().First(&restored, 11).Error; err != nil {
		t.Fatalf("load imported channel: %v", err)
	}
	if len(restored.HeaderRules) != 0 {
		t.Errorf("credential header rule must be dropped, got %#v", restored.HeaderRules)
	}
	if len(restored.JSONRewriteRules) != 0 {
		t.Errorf("invalid override rule must be dropped, got %#v", restored.JSONRewriteRules)
	}
	joined := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joined, "header rules dropped") || !strings.Contains(joined, "json rewrite rules dropped") {
		t.Errorf("dropped rules must be reported, got %#v", result.Warnings)
	}
}
