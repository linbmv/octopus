package op

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"gorm.io/gorm"
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
	if err := db.GetDB().WithContext(ctx).Create(&model.StatsDaily{Date: "20260705", StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}).Error; err != nil {
		t.Fatalf("create stats daily: %v", err)
	}
	if err := db.GetDB().WithContext(ctx).Create(&model.RelayLog{ID: 1, Time: 1, RequestModelName: "gpt-test", OutputTokens: 8, ReasoningTokens: 5}).Error; err != nil {
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
	if len(dump.StatsDaily) != 1 || dump.StatsDaily[0].Date != "20260705" || dump.StatsDaily[0].ReasoningToken != 5 {
		t.Fatalf("stats not exported: %#v", dump.StatsDaily)
	}
	if len(dump.RelayLogs) != 1 || dump.RelayLogs[0].RequestModelName != "gpt-test" || dump.RelayLogs[0].ReasoningTokens != 5 {
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

func TestValidateDBDumpRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.DBDump)
		field  string
	}{
		{name: "missing version", mutate: func(d *model.DBDump) { d.Version = 0 }, field: "version"},
		{name: "unsupported version", mutate: func(d *model.DBDump) { d.Version = 3 }, field: "version"},
		{name: "missing export timestamp", mutate: func(d *model.DBDump) { d.ExportedAt = time.Time{} }, field: "exported_at"},
		{name: "stats contradict flag", mutate: func(d *model.DBDump) { d.IncludeStats = false }, field: "include_stats"},
		{name: "logs contradict flag", mutate: func(d *model.DBDump) { d.IncludeLogs = false }, field: "include_logs"},
		{name: "invalid channel URL", mutate: func(d *model.DBDump) { d.Channels[0].BaseUrls[0].URL = "file:///etc/passwd" }, field: "channels[0].base_urls[0].url"},
		{name: "duplicate channel id", mutate: func(d *model.DBDump) { d.Channels = append(d.Channels, d.Channels[0]) }, field: "channels[1].id"},
		{name: "embedded associations", mutate: func(d *model.DBDump) { d.Channels[0].Keys = []model.ChannelKey{{ID: 999, ChannelKey: "hidden"}} }, field: "channels[0]"},
		{name: "dangling channel key", mutate: func(d *model.DBDump) { d.ChannelKeys[0].ChannelID = 999 }, field: "channel_keys[0].channel_id"},
		{name: "invalid group weight", mutate: func(d *model.DBDump) { d.GroupItems[0].Weight = 0 }, field: "group_items[0].weight"},
		{name: "undeclared group item model", mutate: func(d *model.DBDump) { d.GroupItems[0].ModelName = "not-on-channel" }, field: "group_items[0].model_name"},
		{name: "unknown setting", mutate: func(d *model.DBDump) { d.Settings[0].Key = model.SettingKey("unknown") }, field: "settings[0]"},
		{name: "non-finite LLM price", mutate: func(d *model.DBDump) { d.LLMInfos[0].Input = math.NaN() }, field: "llm_infos[0]"},
		{name: "unknown supported model", mutate: func(d *model.DBDump) { d.APIKeys[0].SupportedModels = "missing-group" }, field: "api_keys[0].supported_models"},
		{name: "dangling channel stats", mutate: func(d *model.DBDump) { d.StatsChannel[0].ChannelID = 999 }, field: "stats_channel[0].channel_id"},
		{name: "negative reasoning stats", mutate: func(d *model.DBDump) { d.StatsTotal[0].ReasoningToken = -1 }, field: "stats_total[0]"},
		{name: "reasoning stats exceed output", mutate: func(d *model.DBDump) { d.StatsTotal[0].ReasoningToken = d.StatsTotal[0].OutputToken + 1 }, field: "stats_total[0].reasoning_token"},
		{name: "negative reasoning log", mutate: func(d *model.DBDump) { d.RelayLogs[0].ReasoningTokens = -1 }, field: "relay_logs[0]"},
		{name: "reasoning log exceeds output", mutate: func(d *model.DBDump) { d.RelayLogs[0].ReasoningTokens = d.RelayLogs[0].OutputTokens + 1 }, field: "relay_logs[0].reasoning_tokens"},
		{
			name: "cyclic group graph",
			mutate: func(d *model.DBDump) {
				d.Groups = []model.Group{
					{ID: 20, Name: "first", Enabled: true, Mode: model.GroupModeRoundRobin},
					{ID: 21, Name: "second", Enabled: true, Mode: model.GroupModeRoundRobin},
				}
				d.GroupItems = []model.GroupItem{
					{ID: 30, GroupID: 20, Type: model.GroupItemTypeGroup, TargetGroupID: 21, Weight: 1},
					{ID: 31, GroupID: 21, Type: model.GroupItemTypeGroup, TargetGroupID: 20, Weight: 1},
				}
			},
			field: "group_items",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dump := validDBDump()
			tt.mutate(dump)
			err := validateDBDump(dump)
			var validationErr *DBImportValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("validateDBDump() error = %v, want DBImportValidationError", err)
			}
			if validationErr.Field != tt.field {
				t.Fatalf("validation field = %q, want %q; error=%v", validationErr.Field, tt.field, err)
			}
		})
	}
}

func TestDBImportRestoreRejectsNonEmptyTarget(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	existing := model.Channel{ID: 1, Name: "existing"}
	if err := db.GetDB().WithContext(ctx).Omit("Keys", "Stats").Create(&existing).Error; err != nil {
		t.Fatalf("create existing channel: %v", err)
	}

	result, err := DBImportRestore(ctx, validDBDump())
	if result != nil {
		t.Fatalf("DBImportRestore() result = %#v, want nil", result)
	}
	var validationErr *DBImportValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "target.channels" {
		t.Fatalf("DBImportRestore() error = %v, want target.channels validation error", err)
	}

	var existingCount, importedCount int64
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", existing.ID).Count(&existingCount).Error; err != nil {
		t.Fatalf("count existing channel: %v", err)
	}
	if err := db.GetDB().Model(&model.Channel{}).Where("id = ?", 10).Count(&importedCount).Error; err != nil {
		t.Fatalf("count imported channel: %v", err)
	}
	if existingCount != 1 || importedCount != 0 {
		t.Fatalf("channel counts after rejected restore = existing:%d imported:%d", existingCount, importedCount)
	}
}

func TestDBImportRestoreRollsBackEveryTableOnLateFailure(t *testing.T) {
	initTestDB(t)
	injected := errors.New("injected settings create failure")
	registerBackupCreateCallback(t, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
			if err := tx.AddError(injected); !errors.Is(err, injected) {
				t.Errorf("AddError() = %v, want injected error", err)
			}
		}
	})

	result, err := DBImportRestore(context.Background(), validDBDump())
	if result != nil || !errors.Is(err, injected) {
		t.Fatalf("DBImportRestore() = (%#v, %v), want injected failure", result, err)
	}
	var validationErr *DBImportValidationError
	if errors.As(err, &validationErr) {
		t.Fatalf("database failure was misclassified as validation: %v", err)
	}

	for _, table := range restoreTargetTables {
		var count int64
		if err := db.GetDB().Model(table.model).Count(&count).Error; err != nil {
			t.Fatalf("count %s after rollback: %v", table.name, err)
		}
		if count != 0 {
			t.Fatalf("table %s has %d rows after rollback", table.name, count)
		}
	}
	var settingCount int64
	if err := db.GetDB().Model(&model.Setting{}).Count(&settingCount).Error; err != nil {
		t.Fatalf("count settings after rollback: %v", err)
	}
	if settingCount != 0 {
		t.Fatalf("settings has %d rows after rollback", settingCount)
	}
}

func TestDBImportRestoreRestoresValidatedDump(t *testing.T) {
	initTestDB(t)
	dump := validDBDump()

	result, err := DBImportRestore(context.Background(), dump)
	if err != nil {
		t.Fatalf("DBImportRestore() error = %v", err)
	}
	if result.Mode != model.DBImportModeEmptyTargetRestore || result.CacheRefreshed {
		t.Fatalf("DBImportRestore() result = %#v", result)
	}
	for _, key := range []string{"channels", "channel_keys", "groups", "group_items", "llm_infos", "api_keys", "settings", "stats_total", "stats_daily", "stats_hourly", "stats_channel", "stats_api_key", "relay_logs"} {
		if result.RowsAffected[key] != 1 {
			t.Errorf("RowsAffected[%q] = %d, want 1", key, result.RowsAffected[key])
		}
	}

	var channel model.Channel
	if err := db.GetDB().First(&channel, 10).Error; err != nil {
		t.Fatalf("load restored channel: %v", err)
	}
	if channel.Name != "source-channel" || len(channel.BaseUrls) != 1 {
		t.Fatalf("restored channel = %#v", channel)
	}
	var groupItem model.GroupItem
	if err := db.GetDB().First(&groupItem, 30).Error; err != nil {
		t.Fatalf("load restored group item: %v", err)
	}
	if groupItem.ChannelID != channel.ID || groupItem.ModelName != "upstream-model" {
		t.Fatalf("restored group item = %#v", groupItem)
	}
	var total model.StatsTotal
	if err := db.GetDB().First(&total, 1).Error; err != nil || total.ReasoningToken != 5 {
		t.Fatalf("restored total reasoning tokens = %d, err=%v", total.ReasoningToken, err)
	}
	var relayLog model.RelayLog
	if err := db.GetDB().First(&relayLog, 50).Error; err != nil || relayLog.ReasoningTokens != 5 {
		t.Fatalf("restored log reasoning tokens = %d, err=%v", relayLog.ReasoningTokens, err)
	}
}

func TestLegacyBackupWithoutReasoningFieldsImportsAsZero(t *testing.T) {
	dump := validDBDump()
	payload, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal current dump: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode current dump document: %v", err)
	}
	for _, table := range []string{"stats_total", "stats_daily", "stats_hourly", "stats_channel", "stats_api_key"} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(document[table], &rows); err != nil {
			t.Fatalf("decode %s: %v", table, err)
		}
		for _, row := range rows {
			delete(row, "reasoning_token")
		}
		document[table], err = json.Marshal(rows)
		if err != nil {
			t.Fatalf("encode legacy %s: %v", table, err)
		}
	}
	var logs []map[string]json.RawMessage
	if err := json.Unmarshal(document["relay_logs"], &logs); err != nil {
		t.Fatalf("decode relay logs: %v", err)
	}
	for _, row := range logs {
		delete(row, "reasoning_tokens")
	}
	document["relay_logs"], err = json.Marshal(logs)
	if err != nil {
		t.Fatalf("encode legacy relay logs: %v", err)
	}
	legacyPayload, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode legacy dump: %v", err)
	}
	var legacy model.DBDump
	if err := json.Unmarshal(legacyPayload, &legacy); err != nil {
		t.Fatalf("decode legacy dump: %v", err)
	}
	if legacy.StatsTotal[0].ReasoningToken != 0 || legacy.RelayLogs[0].ReasoningTokens != 0 {
		t.Fatalf("missing legacy fields did not decode to zero: stats=%d log=%d", legacy.StatsTotal[0].ReasoningToken, legacy.RelayLogs[0].ReasoningTokens)
	}

	initTestDB(t)
	if _, err := DBImportRestore(context.Background(), &legacy); err != nil {
		t.Fatalf("restore legacy dump: %v", err)
	}
	var stats model.StatsTotal
	if err := db.GetDB().First(&stats, 1).Error; err != nil || stats.ReasoningToken != 0 {
		t.Fatalf("legacy total reasoning tokens = %d, err=%v", stats.ReasoningToken, err)
	}
	var relayLog model.RelayLog
	if err := db.GetDB().First(&relayLog, 50).Error; err != nil || relayLog.ReasoningTokens != 0 {
		t.Fatalf("legacy log reasoning tokens = %d, err=%v", relayLog.ReasoningTokens, err)
	}
}

func TestDBExportThenRestoreIntoNewDatabaseRoundTrip(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	if _, err := DBImportRestore(ctx, validDBDump()); err != nil {
		t.Fatalf("seed source with validated restore: %v", err)
	}

	var exported bytes.Buffer
	if err := DBExportAllStream(ctx, &exported, true, true); err != nil {
		t.Fatalf("export source database: %v", err)
	}
	var dump model.DBDump
	if err := json.Unmarshal(exported.Bytes(), &dump); err != nil {
		t.Fatalf("decode exported database: %v", err)
	}
	if len(dump.Channels) != 1 || len(dump.Channels[0].Keys) != 0 || dump.Channels[0].Stats != nil {
		t.Fatalf("exported channel contains unexpected associations: %#v", dump.Channels)
	}
	if len(dump.Groups) != 1 || len(dump.Groups[0].Items) != 0 {
		t.Fatalf("exported group contains unexpected associations: %#v", dump.Groups)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "roundtrip-target.db"), false); err != nil {
		t.Fatalf("init roundtrip target database: %v", err)
	}
	result, err := DBImportRestore(ctx, &dump)
	if err != nil {
		t.Fatalf("restore exported dump: %v", err)
	}
	if result.Mode != model.DBImportModeEmptyTargetRestore {
		t.Fatalf("roundtrip mode = %q", result.Mode)
	}

	var channel model.Channel
	if err := db.GetDB().First(&channel, 10).Error; err != nil {
		t.Fatalf("load roundtrip channel: %v", err)
	}
	var key model.ChannelKey
	if err := db.GetDB().First(&key, 11).Error; err != nil {
		t.Fatalf("load roundtrip channel key: %v", err)
	}
	if channel.Name != "source-channel" || key.ChannelID != channel.ID || key.ChannelKey != "sk-upstream" {
		t.Fatalf("roundtrip relation mismatch: channel=%#v key=%#v", channel, key)
	}
}

func TestDBImportV2DryRunAndNumericIDRemapping(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	existing := model.Channel{
		ID:       10,
		Name:     "target-only-channel",
		Type:     llm.APIFormatOpenAIChatCompletion,
		Enabled:  true,
		BaseUrls: []model.BaseUrl{{URL: "https://target.example.test/v1"}},
		Model:    "target-model",
	}
	if err := db.GetDB().Omit("Keys", "Stats").Create(&existing).Error; err != nil {
		t.Fatalf("create target numeric-ID collision: %v", err)
	}
	dump := validDBDump()

	dryRun, err := DBImportV2(ctx, dump, model.DBImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run import: %v", err)
	}
	if !dryRun.DryRun || dryRun.Mode != model.DBImportModeIncremental || dryRun.ConflictPolicy != model.DBImportConflictReject {
		t.Fatalf("dry-run metadata = %#v", dryRun)
	}
	if dryRun.Tables["channels"].Create != 1 || dryRun.Tables["channel_keys"].Create != 1 || dryRun.Tables["group_items"].Create != 1 {
		t.Fatalf("dry-run plan = %#v", dryRun.Tables)
	}
	var sourceCount int64
	if err := db.GetDB().Model(&model.Channel{}).Where("uuid = ?", dump.Channels[0].UUID).Count(&sourceCount).Error; err != nil {
		t.Fatalf("count dry-run source channel: %v", err)
	}
	if sourceCount != 0 {
		t.Fatalf("dry-run wrote %d source channels", sourceCount)
	}

	result, err := DBImportV2(ctx, dump, model.DBImportOptions{})
	if err != nil {
		t.Fatalf("incremental import: %v", err)
	}
	if result.DryRun || result.RowsAffected["channels"] != 1 {
		t.Fatalf("incremental result = %#v", result)
	}
	var importedChannel model.Channel
	if err := db.GetDB().Where("uuid = ?", dump.Channels[0].UUID).First(&importedChannel).Error; err != nil {
		t.Fatalf("load remapped channel: %v", err)
	}
	if importedChannel.ID == dump.Channels[0].ID || importedChannel.ID == existing.ID {
		t.Fatalf("source numeric channel ID was reused: imported=%d source=%d", importedChannel.ID, dump.Channels[0].ID)
	}
	var importedKey model.ChannelKey
	if err := db.GetDB().Where("uuid = ?", dump.ChannelKeys[0].UUID).First(&importedKey).Error; err != nil {
		t.Fatalf("load remapped channel key: %v", err)
	}
	if importedKey.ChannelID != importedChannel.ID {
		t.Fatalf("channel key relation = %d, want %d", importedKey.ChannelID, importedChannel.ID)
	}
	var importedItem model.GroupItem
	if err := db.GetDB().Where("uuid = ?", dump.GroupItems[0].UUID).First(&importedItem).Error; err != nil {
		t.Fatalf("load remapped group item: %v", err)
	}
	if importedItem.ChannelID != importedChannel.ID {
		t.Fatalf("group item channel relation = %d, want %d", importedItem.ChannelID, importedChannel.ID)
	}
	var channelStats model.StatsChannel
	if err := db.GetDB().Where("channel_id = ?", importedChannel.ID).First(&channelStats).Error; err != nil {
		t.Fatalf("load remapped channel stats: %v", err)
	}
	if channelStats.ReasoningToken != 5 {
		t.Fatalf("remapped channel reasoning tokens = %d, want 5", channelStats.ReasoningToken)
	}
	var relayLog model.RelayLog
	if err := db.GetDB().First(&relayLog, 50).Error; err != nil {
		t.Fatalf("load remapped relay log: %v", err)
	}
	if relayLog.ChannelId != importedChannel.ID || len(relayLog.Attempts) != 1 || relayLog.Attempts[0].ChannelKeyID != importedKey.ID {
		t.Fatalf("relay log relations were not remapped: %#v", relayLog)
	}
	if relayLog.ReasoningTokens != 5 {
		t.Fatalf("remapped log reasoning tokens = %d, want 5", relayLog.ReasoningTokens)
	}
}

func TestDBImportV2ConflictPoliciesAreRepeatable(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	dump := validDBDump()
	if _, err := DBImportV2(ctx, dump, model.DBImportOptions{}); err != nil {
		t.Fatalf("seed v2 import: %v", err)
	}

	rejected, err := DBImportV2(ctx, dump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictReject})
	if !errors.Is(err, ErrDBImportConflict) || rejected == nil || rejected.Tables["channels"].Conflict != 1 {
		t.Fatalf("reject repeat = (%#v, %v)", rejected, err)
	}
	skipped, err := DBImportV2(ctx, dump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictSkip})
	if err != nil {
		t.Fatalf("skip repeat: %v", err)
	}
	if skipped.Tables["channels"].Skip != 1 || skipped.Tables["channel_keys"].Skip != 1 {
		t.Fatalf("skip result = %#v", skipped.Tables)
	}

	var channel model.Channel
	if err := db.GetDB().Where("uuid = ?", dump.Channels[0].UUID).First(&channel).Error; err != nil {
		t.Fatalf("load imported channel: %v", err)
	}
	var group model.Group
	if err := db.GetDB().Where("uuid = ?", dump.Groups[0].UUID).First(&group).Error; err != nil {
		t.Fatalf("load imported group: %v", err)
	}
	extraKey := model.ChannelKey{ChannelID: channel.ID, Enabled: true, ChannelKey: "target-only-key"}
	if err := db.GetDB().Create(&extraKey).Error; err != nil {
		t.Fatalf("create target-only channel key: %v", err)
	}
	extraItem := model.GroupItem{
		GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: channel.ID,
		ModelName: "target-only-model", Priority: 9, Weight: 1,
	}
	if err := db.GetDB().Create(&extraItem).Error; err != nil {
		t.Fatalf("create target-only group item: %v", err)
	}

	mergedDump := cloneDBDump(t, dump)
	mergedDump.Channels[0].Name = "source-channel-merged"
	mergedDump.StatsTotal[0].OutputToken = 9
	mergedDump.StatsTotal[0].ReasoningToken = 6
	mergedDump.RelayLogs[0].OutputTokens = 9
	mergedDump.RelayLogs[0].ReasoningTokens = 6
	merged, err := DBImportV2(ctx, mergedDump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictMerge})
	if err != nil {
		t.Fatalf("merge repeat: %v", err)
	}
	if merged.Tables["channels"].Update != 1 || merged.Tables["channel_keys"].Delete != 0 || merged.Tables["group_items"].Delete != 0 {
		t.Fatalf("merge result = %#v", merged.Tables)
	}
	for table, id := range map[string]int{"channel_keys": extraKey.ID, "group_items": extraItem.ID} {
		var count int64
		if err := db.GetDB().Table(table).Where("id = ?", id).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("merge did not preserve %s row %d: count=%d err=%v", table, id, count, err)
		}
	}
	var mergedStats model.StatsTotal
	if err := db.GetDB().First(&mergedStats, 1).Error; err != nil || mergedStats.ReasoningToken != 6 {
		t.Fatalf("merge total reasoning tokens = %d, err=%v", mergedStats.ReasoningToken, err)
	}
	var mergedLog model.RelayLog
	if err := db.GetDB().First(&mergedLog, 50).Error; err != nil || mergedLog.ReasoningTokens != 5 {
		t.Fatalf("merge should preserve conflicting log reasoning tokens = %d, err=%v", mergedLog.ReasoningTokens, err)
	}

	replaced, err := DBImportV2(ctx, mergedDump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictReplace})
	if err != nil {
		t.Fatalf("replace repeat: %v", err)
	}
	if replaced.Tables["channel_keys"].Delete != 1 || replaced.Tables["group_items"].Delete != 1 {
		t.Fatalf("replace result = %#v", replaced.Tables)
	}
	for table, id := range map[string]int{"channel_keys": extraKey.ID, "group_items": extraItem.ID} {
		var count int64
		if err := db.GetDB().Table(table).Where("id = ?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("replace retained %s row %d: count=%d err=%v", table, id, count, err)
		}
	}
	var replacedLog model.RelayLog
	if err := db.GetDB().First(&replacedLog, 50).Error; err != nil || replacedLog.ReasoningTokens != 6 {
		t.Fatalf("replace log reasoning tokens = %d, err=%v", replacedLog.ReasoningTokens, err)
	}
}

func TestDBImportV2ReportsAmbiguousStableIdentity(t *testing.T) {
	initTestDB(t)
	dump := validDBDump()
	uuidOwner := dump.Channels[0]
	uuidOwner.ID = 0
	uuidOwner.Name = "uuid-owner"
	nameOwner := dump.Channels[0]
	nameOwner.ID = 0
	nameOwner.UUID = "00000000-0000-4000-8000-000000000099"
	if err := db.GetDB().Omit("Keys", "Stats").Create(&uuidOwner).Error; err != nil {
		t.Fatalf("create UUID owner: %v", err)
	}
	if err := db.GetDB().Omit("Keys", "Stats").Create(&nameOwner).Error; err != nil {
		t.Fatalf("create name owner: %v", err)
	}

	dryRun, err := DBImportV2(context.Background(), dump, model.DBImportOptions{
		DryRun: true, ConflictPolicy: model.DBImportConflictMerge,
	})
	if err != nil {
		t.Fatalf("ambiguous dry-run: %v", err)
	}
	if dryRun.Tables["channels"].Unresolved != 1 || len(dryRun.Issues) == 0 || dryRun.Issues[0].Field != "name" {
		t.Fatalf("ambiguous dry-run result = %#v", dryRun)
	}

	result, err := DBImportV2(context.Background(), dump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictMerge})
	if !errors.Is(err, ErrDBImportConflict) || result == nil || result.Tables["channels"].Unresolved != 1 {
		t.Fatalf("ambiguous import = (%#v, %v)", result, err)
	}
	var sourceNameCount int64
	if err := db.GetDB().Model(&model.Channel{}).Where("name = ?", "source-channel").Count(&sourceNameCount).Error; err != nil {
		t.Fatalf("count ambiguous source name: %v", err)
	}
	if sourceNameCount != 1 {
		t.Fatalf("ambiguous import changed target identity rows: count=%d", sourceNameCount)
	}
}

func TestDBImportV2RejectsCycleCreatedByMerge(t *testing.T) {
	initTestDB(t)
	const (
		groupAUUID     = "00000000-0000-4000-8000-0000000000a1"
		groupBUUID     = "00000000-0000-4000-8000-0000000000b1"
		dumpItemUUID   = "00000000-0000-4000-8000-0000000000c1"
		targetItemUUID = "00000000-0000-4000-8000-0000000000d1"
	)
	dump := &model.DBDump{
		Version:    dbDumpVersion,
		ExportedAt: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		Groups: []model.Group{
			{ID: 1, UUID: groupAUUID, Name: "group-a", Enabled: true, Mode: model.GroupModeRoundRobin},
			{ID: 2, UUID: groupBUUID, Name: "group-b", Enabled: true, Mode: model.GroupModeRoundRobin},
		},
		GroupItems: []model.GroupItem{{
			ID: 3, UUID: dumpItemUUID, GroupID: 1, Type: model.GroupItemTypeGroup,
			TargetGroupID: 2, Weight: 1,
		}},
		Relations: &model.DBDumpRelationsV2{
			ChannelKeys: map[string]string{},
			GroupItems: map[string]model.DBDumpGroupItemRelation{
				dumpItemUUID: {GroupUUID: groupAUUID, TargetGroupUUID: groupBUUID},
			},
		},
	}
	targetGroups := append([]model.Group(nil), dump.Groups...)
	for i := range targetGroups {
		targetGroups[i].ID = 0
	}
	if err := db.GetDB().Create(&targetGroups).Error; err != nil {
		t.Fatalf("create target groups: %v", err)
	}
	targetOnly := model.GroupItem{
		UUID: targetItemUUID, GroupID: targetGroups[1].ID, Type: model.GroupItemTypeGroup,
		TargetGroupID: targetGroups[0].ID, Weight: 1,
	}
	if err := db.GetDB().Create(&targetOnly).Error; err != nil {
		t.Fatalf("create target-only nested item: %v", err)
	}

	dryRun, err := DBImportV2(context.Background(), dump, model.DBImportOptions{
		DryRun: true, ConflictPolicy: model.DBImportConflictMerge,
	})
	if err != nil {
		t.Fatalf("cycle dry-run: %v", err)
	}
	if dryRun.Tables["group_items"].Unresolved != 1 {
		t.Fatalf("cycle dry-run result = %#v", dryRun)
	}
	result, err := DBImportV2(context.Background(), dump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictMerge})
	if !errors.Is(err, ErrDBImportConflict) || result == nil {
		t.Fatalf("cycle import = (%#v, %v)", result, err)
	}
	var importedCount int64
	if err := db.GetDB().Model(&model.GroupItem{}).Where("uuid = ?", dumpItemUUID).Count(&importedCount).Error; err != nil {
		t.Fatalf("count rolled-back cycle item: %v", err)
	}
	if importedCount != 0 {
		t.Fatalf("cycle import committed %d source items", importedCount)
	}
}

func TestDBImportV2RollsBackOnLateFailure(t *testing.T) {
	initTestDB(t)
	injected := errors.New("injected v2 settings failure")
	registerBackupCreateCallback(t, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
			_ = tx.AddError(injected)
		}
	})

	result, err := DBImportV2(context.Background(), validDBDump(), model.DBImportOptions{})
	if result != nil || !errors.Is(err, injected) {
		t.Fatalf("late failure = (%#v, %v), want injected rollback", result, err)
	}
	for _, table := range restoreTargetTables {
		var count int64
		if err := db.GetDB().Model(table.model).Count(&count).Error; err != nil {
			t.Fatalf("count rolled-back %s: %v", table.name, err)
		}
		if count != 0 {
			t.Fatalf("late failure left %d rows in %s", count, table.name)
		}
	}
}

func cloneDBDump(t *testing.T, source *model.DBDump) *model.DBDump {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal dump clone: %v", err)
	}
	var clone model.DBDump
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatalf("unmarshal dump clone: %v", err)
	}
	return &clone
}

func validDBDump() *model.DBDump {
	return &model.DBDump{
		Version:      dbDumpVersion,
		ExportedAt:   time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
		IncludeLogs:  true,
		IncludeStats: true,
		Channels: []model.Channel{{
			ID:       10,
			UUID:     "00000000-0000-4000-8000-000000000010",
			Name:     "source-channel",
			Type:     llm.APIFormatOpenAIChatCompletion,
			Enabled:  true,
			BaseUrls: []model.BaseUrl{{URL: "https://api.example.test/v1", Delay: 1}},
			Model:    "upstream-model",
		}},
		ChannelKeys: []model.ChannelKey{{
			ID: 11, UUID: "00000000-0000-4000-8000-000000000011", ChannelID: 10, Enabled: true, ChannelKey: "sk-upstream", Remark: "primary",
		}},
		Groups: []model.Group{{
			ID: 20, UUID: "00000000-0000-4000-8000-000000000020", Name: "client-model", Enabled: true, Mode: model.GroupModeRoundRobin,
		}},
		GroupItems: []model.GroupItem{{
			ID: 30, UUID: "00000000-0000-4000-8000-000000000030", GroupID: 20, Type: model.GroupItemTypeChannel, ChannelID: 10, ModelName: "upstream-model", Weight: 1,
		}},
		LLMInfos: []model.LLMInfo{{Name: "upstream-model", LLMPrice: model.LLMPrice{Input: 1, Output: 2}}},
		APIKeys: []model.APIKey{{
			ID: 40, UUID: "00000000-0000-4000-8000-000000000040", Name: "client", APIKey: "sk-client", Enabled: true, MaxCost: 100, SupportedModels: "client-model",
		}},
		Relations: &model.DBDumpRelationsV2{
			ChannelKeys: map[string]string{
				"00000000-0000-4000-8000-000000000011": "00000000-0000-4000-8000-000000000010",
			},
			GroupItems: map[string]model.DBDumpGroupItemRelation{
				"00000000-0000-4000-8000-000000000030": {
					GroupUUID:   "00000000-0000-4000-8000-000000000020",
					ChannelUUID: "00000000-0000-4000-8000-000000000010",
				},
			},
		},
		Settings:     []model.Setting{{Key: model.SettingKeyRelayLogKeepEnabled, Value: "true"}},
		StatsTotal:   []model.StatsTotal{{ID: 1, StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}},
		StatsDaily:   []model.StatsDaily{{Date: "20260715", StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}},
		StatsHourly:  []model.StatsHourly{{Hour: 12, Date: "20260715", StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}},
		StatsChannel: []model.StatsChannel{{ChannelID: 10, StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}},
		StatsAPIKey:  []model.StatsAPIKey{{APIKeyID: 40, StatsMetrics: model.StatsMetrics{OutputToken: 8, ReasoningToken: 5, RequestSuccess: 1}}},
		RelayLogs: []model.RelayLog{{
			ID: 50, Time: 1, RequestModelName: "client-model", RequestAPIKeyName: "client", ChannelId: 10,
			ChannelName: "source-channel", ActualModelName: "upstream-model", OutputTokens: 8, ReasoningTokens: 5, TotalAttempts: 1,
			Attempts: []model.ChannelAttempt{{
				ChannelID: 10, ChannelKeyID: 11, ChannelName: "source-channel", ModelName: "upstream-model",
				AttemptNum: 1, Status: model.AttemptSuccess,
			}},
		}},
	}
}

func registerBackupCreateCallback(t *testing.T, callback func(*gorm.DB)) {
	t.Helper()
	processor := db.GetDB().Callback().Create()
	name := fmt.Sprintf("test:backup-restore:%s", strings.ReplaceAll(t.Name(), "/", ":"))
	if err := processor.Before("gorm:create").Register(name, callback); err != nil {
		t.Fatalf("register backup create callback: %v", err)
	}
	t.Cleanup(func() {
		if err := processor.Remove(name); err != nil {
			t.Errorf("remove backup create callback: %v", err)
		}
	})
}
