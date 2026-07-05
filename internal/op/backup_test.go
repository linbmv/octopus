package op

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestDBExportAllStreamProducesImportCompatibleDump(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	channel := model.Channel{ID: 1, Name: "stream-export", Enabled: true}
	if err := db.GetDB().WithContext(ctx).Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.ChannelKey{ID: 1, ChannelID: channel.ID, Enabled: true, ChannelKey: "test-key"}).Error; err != nil {
		t.Fatalf("create channel key: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.Setting{Key: model.SettingKeyRelayLogKeepEnabled, Value: "true"}).Error; err != nil {
		t.Fatalf("create setting: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.StatsDaily{Date: "20260705", StatsMetrics: model.StatsMetrics{RequestSuccess: 1}}).Error; err != nil {
		t.Fatalf("create stats daily: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.RelayLog{ID: 1, Time: 1, RequestModelName: "gpt-test"}).Error; err != nil {
		t.Fatalf("create relay log: %v", err)
	}

	var buf bytes.Buffer
	if err := DBExportAllStream(ctx, &buf, true, true); err != nil {
		t.Fatalf("stream export: %v", err)
	}

	var dump model.DBDump
	if err := json.Unmarshal(buf.Bytes(), &dump); err != nil {
		t.Fatalf("unmarshal streamed dump: %v\n%s", err, buf.String())
	}

	if dump.Version != dbDumpVersion || !dump.IncludeLogs || !dump.IncludeStats {
		t.Fatalf("dump metadata mismatch: version=%d include_logs=%t include_stats=%t", dump.Version, dump.IncludeLogs, dump.IncludeStats)
	}
	if len(dump.Channels) != 1 || dump.Channels[0].Name != channel.Name {
		t.Fatalf("channels not exported: %#v", dump.Channels)
	}
	if len(dump.ChannelKeys) != 1 || dump.ChannelKeys[0].ChannelKey != "test-key" {
		t.Fatalf("channel keys not exported: %#v", dump.ChannelKeys)
	}
	if len(dump.Settings) != 1 || dump.Settings[0].Key != model.SettingKeyRelayLogKeepEnabled {
		t.Fatalf("settings not exported: %#v", dump.Settings)
	}
	if len(dump.StatsDaily) != 1 || dump.StatsDaily[0].Date != "20260705" {
		t.Fatalf("stats not exported: %#v", dump.StatsDaily)
	}
	if len(dump.RelayLogs) != 1 || dump.RelayLogs[0].RequestModelName != "gpt-test" {
		t.Fatalf("relay logs not exported: %#v", dump.RelayLogs)
	}
}

func TestDBExportAllStreamRespectsOptionalTables(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	if err := db.GetDB().WithContext(ctx).Create(&model.StatsDaily{Date: "20260705"}).Error; err != nil {
		t.Fatalf("create stats daily: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.RelayLog{ID: 1, Time: 1}).Error; err != nil {
		t.Fatalf("create relay log: %v", err)
	}

	var buf bytes.Buffer
	if err := DBExportAllStream(ctx, &buf, false, false); err != nil {
		t.Fatalf("stream export: %v", err)
	}

	var dump model.DBDump
	if err := json.Unmarshal(buf.Bytes(), &dump); err != nil {
		t.Fatalf("unmarshal streamed dump: %v", err)
	}
	if dump.IncludeLogs || dump.IncludeStats {
		t.Fatalf("optional flags mismatch: logs=%t stats=%t", dump.IncludeLogs, dump.IncludeStats)
	}
	if len(dump.RelayLogs) != 0 || len(dump.StatsDaily) != 0 {
		t.Fatalf("optional tables should be omitted: relay_logs=%d stats_daily=%d", len(dump.RelayLogs), len(dump.StatsDaily))
	}
}
