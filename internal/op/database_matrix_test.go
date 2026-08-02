package op

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/db/migrate"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"gorm.io/gorm"
)

// TestDatabaseMatrix is an opt-in integration test. Each invocation exercises
// one real dialect against two independent, empty, disposable databases: a
// source populated by the test and a fresh restore target. The test writes to
// both databases and intentionally does not delete externally owned data, so
// do not reuse either DSN for another invocation. Keeping the DSNs in
// environment variables avoids embedding CI or developer credentials in the
// repository.
//
// Example:
//
//	OCTOPUS_TEST_DB_TYPE=postgres \
//	OCTOPUS_TEST_DB_SOURCE_DSN='host=127.0.0.1 ... dbname=octopus_source' \
//	OCTOPUS_TEST_DB_TARGET_DSN='host=127.0.0.1 ... dbname=octopus_target' \
//	go test -count=1 ./internal/op -run '^TestDatabaseMatrix$'
func TestDatabaseMatrix(t *testing.T) {
	dbType := strings.TrimSpace(os.Getenv("OCTOPUS_TEST_DB_TYPE"))
	sourceDSN := os.Getenv("OCTOPUS_TEST_DB_SOURCE_DSN")
	targetDSN := os.Getenv("OCTOPUS_TEST_DB_TARGET_DSN")
	if dbType == "" && sourceDSN == "" && targetDSN == "" {
		t.Skip("set OCTOPUS_TEST_DB_TYPE, OCTOPUS_TEST_DB_SOURCE_DSN, and OCTOPUS_TEST_DB_TARGET_DSN to run the database matrix")
	}
	if dbType == "" || sourceDSN == "" || targetDSN == "" {
		t.Fatal("OCTOPUS_TEST_DB_TYPE, OCTOPUS_TEST_DB_SOURCE_DSN, and OCTOPUS_TEST_DB_TARGET_DSN must be set together")
	}
	if sourceDSN == targetDSN {
		t.Fatal("source and target DSNs must identify independent databases")
	}

	matrixResetRuntimeState()
	t.Cleanup(func() {
		if db.GetDB() != nil {
			_ = db.Close()
		}
		matrixResetRuntimeState()
	})

	ctx := context.Background()
	if err := db.InitDB(dbType, sourceDSN, false); err != nil {
		t.Fatalf("initialize source %s database: %v", dbType, err)
	}
	assertDatabaseMatrixSchemaAndMigrations(t, dbType)

	legacyDump := cloneDBDump(t, validDBDump())
	legacyDump.Version = dbDumpLegacyVersion
	legacyDump.Relations = nil
	for i := range legacyDump.Channels {
		legacyDump.Channels[i].UUID = ""
	}
	for i := range legacyDump.ChannelKeys {
		legacyDump.ChannelKeys[i].UUID = ""
	}
	for i := range legacyDump.Groups {
		legacyDump.Groups[i].UUID = ""
	}
	for i := range legacyDump.GroupItems {
		legacyDump.GroupItems[i].UUID = ""
	}
	for i := range legacyDump.APIKeys {
		legacyDump.APIKeys[i].UUID = ""
	}
	if _, err := DBImportRestore(ctx, legacyDump); err != nil {
		t.Fatalf("seed source database: %v", err)
	}
	var exported bytes.Buffer
	if err := DBExportAllStream(ctx, &exported, true, true); err != nil {
		t.Fatalf("export source database: %v", err)
	}
	var dump model.DBDump
	if err := json.Unmarshal(exported.Bytes(), &dump); err != nil {
		t.Fatalf("decode source export: %v", err)
	}
	if len(dump.Channels) != 1 || len(dump.ChannelKeys) != 1 || len(dump.Groups) != 1 || len(dump.GroupItems) != 1 {
		t.Fatalf("source export omitted core rows: channels=%d keys=%d groups=%d items=%d",
			len(dump.Channels), len(dump.ChannelKeys), len(dump.Groups), len(dump.GroupItems))
	}
	if dump.Version != dbDumpVersion || dump.Relations == nil || dump.Channels[0].UUID == "" || dump.ChannelKeys[0].UUID == "" ||
		dump.Groups[0].UUID == "" || dump.GroupItems[0].UUID == "" || dump.APIKeys[0].UUID == "" {
		t.Fatalf("v1 restore did not export a complete v2 identity graph: %#v", dump)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	matrixResetRuntimeState()
	if err := db.InitDB(dbType, targetDSN, false); err != nil {
		t.Fatalf("initialize empty target %s database: %v", dbType, err)
	}
	assertDatabaseMatrixSchemaAndMigrations(t, dbType)
	assertDatabaseMatrixRestoreTablesEmpty(t)

	t.Run("late failure rolls back every restored table", func(t *testing.T) {
		injected := errors.New("database matrix injected settings failure")
		registerBackupCreateCallback(t, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
				_ = tx.AddError(injected)
			}
		})

		result, err := DBImportRestore(ctx, &dump)
		if result != nil || !errors.Is(err, injected) {
			t.Fatalf("restore with injected failure = (%#v, %v), want rollback error", result, err)
		}
		assertDatabaseMatrixRestoreTablesEmpty(t)
	})

	result, err := DBImportRestore(ctx, &dump)
	if err != nil {
		t.Fatalf("restore export into empty target: %v", err)
	}
	if result.Mode != model.DBImportModeEmptyTargetRestore {
		t.Fatalf("restore mode = %q, want %q", result.Mode, model.DBImportModeEmptyTargetRestore)
	}
	for _, table := range []string{
		"channels", "channel_keys", "groups", "group_items", "llm_infos", "api_keys", "settings",
		"stats_total", "stats_daily", "stats_hourly", "stats_channel", "stats_api_key", "relay_logs",
	} {
		if result.RowsAffected[table] != 1 {
			t.Errorf("restored rows for %s = %d, want 1", table, result.RowsAffected[table])
		}
	}

	var restoredChannel model.Channel
	if err := db.GetDB().Preload("Keys").First(&restoredChannel, 10).Error; err != nil {
		t.Fatalf("read restored channel: %v", err)
	}
	if restoredChannel.Name != "source-channel" || len(restoredChannel.Keys) != 1 || restoredChannel.Keys[0].ChannelKey != "sk-upstream" {
		t.Fatalf("restored channel relation mismatch: %#v", restoredChannel)
	}
	var restoredStats model.StatsTotal
	if err := db.GetDB().First(&restoredStats, 1).Error; err != nil || restoredStats.ReasoningToken != 5 {
		t.Fatalf("restored total reasoning tokens = %d, err=%v", restoredStats.ReasoningToken, err)
	}
	var restoredLog model.RelayLog
	if err := db.GetDB().First(&restoredLog, 50).Error; err != nil || restoredLog.ReasoningTokens != 5 {
		t.Fatalf("restored log reasoning tokens = %d, err=%v", restoredLog.ReasoningTokens, err)
	}

	dryRun, err := DBImportV2(ctx, &dump, model.DBImportOptions{DryRun: true, ConflictPolicy: model.DBImportConflictReplace})
	if err != nil {
		t.Fatalf("dry-run repeated v2 import: %v", err)
	}
	if dryRun.Tables["channels"].Update != 1 || dryRun.Tables["channel_keys"].Update != 1 {
		t.Fatalf("dry-run repeated v2 plan = %#v", dryRun.Tables)
	}
	var channelCountAfterDryRun int64
	if err := db.GetDB().Model(&model.Channel{}).Count(&channelCountAfterDryRun).Error; err != nil || channelCountAfterDryRun != 1 {
		t.Fatalf("dry-run changed target channels: count=%d err=%v", channelCountAfterDryRun, err)
	}
	if skipped, err := DBImportV2(ctx, &dump, model.DBImportOptions{ConflictPolicy: model.DBImportConflictSkip}); err != nil {
		t.Fatalf("skip repeated v2 import: %v", err)
	} else if skipped.Tables["channels"].Skip != 1 || skipped.Tables["channel_keys"].Skip != 1 {
		t.Fatalf("skip repeated v2 result = %#v", skipped.Tables)
	}

	t.Run("incremental late failure rolls back updates", func(t *testing.T) {
		injected := errors.New("database matrix injected incremental settings failure")
		registerBackupUpdateCallback(t, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "settings" {
				_ = tx.AddError(injected)
			}
		})
		changed := cloneDBDump(t, &dump)
		changed.Channels[0].Name = "must-roll-back"
		result, err := DBImportV2(ctx, changed, model.DBImportOptions{ConflictPolicy: model.DBImportConflictReplace})
		if result != nil || !errors.Is(err, injected) {
			t.Fatalf("incremental failure = (%#v, %v), want injected rollback", result, err)
		}
		var channel model.Channel
		if err := db.GetDB().Where("uuid = ?", dump.Channels[0].UUID).First(&channel).Error; err != nil {
			t.Fatalf("read rolled-back incremental channel: %v", err)
		}
		if channel.Name != dump.Channels[0].Name {
			t.Fatalf("incremental failure committed channel name %q", channel.Name)
		}
	})

	incremental := matrixIncrementalDump(t, &dump)
	if merged, err := DBImportV2(ctx, incremental, model.DBImportOptions{ConflictPolicy: model.DBImportConflictMerge}); err != nil {
		t.Fatalf("merge numeric-ID-conflicting v2 dump: %v", err)
	} else if merged.Tables["channels"].Create != 1 || merged.Tables["channel_keys"].Create != 1 || merged.Tables["group_items"].Create != 1 {
		t.Fatalf("incremental merge result = %#v", merged.Tables)
	}
	var incrementalChannel model.Channel
	if err := db.GetDB().Where("uuid = ?", incremental.Channels[0].UUID).First(&incrementalChannel).Error; err != nil {
		t.Fatalf("read incrementally imported channel: %v", err)
	}
	if incrementalChannel.ID == incremental.Channels[0].ID || incrementalChannel.ID == restoredChannel.ID {
		t.Fatalf("incremental import reused source numeric ID %d", incrementalChannel.ID)
	}
	var incrementalKey model.ChannelKey
	if err := db.GetDB().Where("uuid = ?", incremental.ChannelKeys[0].UUID).First(&incrementalKey).Error; err != nil {
		t.Fatalf("read incrementally imported channel key: %v", err)
	}
	if incrementalKey.ChannelID != incrementalChannel.ID {
		t.Fatalf("incremental key relation = %d, want %d", incrementalKey.ChannelID, incrementalChannel.ID)
	}

	if err := InitCache(); err != nil {
		t.Fatalf("initialize runtime caches from restored target: %v", err)
	}
	assertDatabaseMatrixCRUDAndSequences(t, ctx)
	assertDatabaseMatrixConcurrentDeletes(t, ctx)
}

func assertDatabaseMatrixSchemaAndMigrations(t *testing.T, dbType string) {
	t.Helper()
	conn := db.GetDB()
	if conn == nil {
		t.Fatal("database connection is nil")
		return
	}
	wantDialect := dbType
	if wantDialect == "postgresql" {
		wantDialect = "postgres"
	}
	if conn.Name() != wantDialect {
		t.Fatalf("database dialect = %q, want %q", conn.Name(), wantDialect)
	}

	for _, table := range []any{
		&model.User{}, &model.WebAuthnCredential{}, &model.Channel{}, &model.ChannelKey{}, &model.Group{}, &model.GroupItem{},
		&model.LLMInfo{}, &model.APIKey{}, &model.Setting{}, &model.StatsTotal{}, &model.StatsDaily{},
		&model.StatsHourly{}, &model.StatsChannel{}, &model.StatsAPIKey{}, &model.RelayLog{},
		&migrate.MigrationRecord{},
	} {
		if !conn.Migrator().HasTable(table) {
			t.Errorf("migrated table for %T is missing", table)
		}
	}

	var records []migrate.MigrationRecord
	if err := conn.Order("version ASC").Find(&records).Error; err != nil {
		t.Fatalf("read migration records: %v", err)
	}
	wantVersions := []int{1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	gotVersions := make([]int, len(records))
	for i, record := range records {
		gotVersions[i] = record.Version
		if record.Status != migrate.MigrationRecordStatusSuccess {
			t.Errorf("migration %d status = %d, want success", record.Version, record.Status)
		}
	}
	if !slices.Equal(gotVersions, wantVersions) {
		t.Fatalf("recorded migration versions = %v, want %v", gotVersions, wantVersions)
	}
	for _, column := range []struct {
		model any
		field string
	}{
		{model: &model.StatsTotal{}, field: "ReasoningToken"},
		{model: &model.StatsDaily{}, field: "ReasoningToken"},
		{model: &model.StatsHourly{}, field: "ReasoningToken"},
		{model: &model.StatsChannel{}, field: "ReasoningToken"},
		{model: &model.StatsAPIKey{}, field: "ReasoningToken"},
		{model: &model.RelayLog{}, field: "ReasoningTokens"},
	} {
		if !conn.Migrator().HasColumn(column.model, column.field) {
			t.Errorf("reasoning token column %T.%s is missing", column.model, column.field)
		}
	}
}

func assertDatabaseMatrixRestoreTablesEmpty(t *testing.T) {
	t.Helper()
	for _, table := range restoreTargetTables {
		var count int64
		if err := db.GetDB().Model(table.model).Count(&count).Error; err != nil {
			t.Fatalf("count empty target table %s: %v", table.name, err)
		}
		if count != 0 {
			t.Fatalf("empty target table %s contains %d rows", table.name, count)
		}
	}
	var settingCount int64
	if err := db.GetDB().Model(&model.Setting{}).Count(&settingCount).Error; err != nil {
		t.Fatalf("count empty target settings: %v", err)
	}
	if settingCount != 0 {
		t.Fatalf("empty target settings contains %d rows", settingCount)
	}
}

func assertDatabaseMatrixCRUDAndSequences(t *testing.T, ctx context.Context) {
	t.Helper()
	channel := model.Channel{
		Name:     "matrix-crud-channel",
		Type:     llm.APIFormatOpenAIChatCompletion,
		Enabled:  true,
		BaseUrls: []model.BaseUrl{{URL: "https://matrix.example.test/v1"}},
		Model:    "matrix-upstream-model",
		Keys: []model.ChannelKey{{
			Enabled: true, ChannelKey: "sk-matrix-upstream", Remark: "matrix",
		}},
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("create channel through operation layer: %v", err)
	}
	if channel.ID <= 10 || len(channel.Keys) != 1 || channel.Keys[0].ID <= 11 {
		t.Fatalf("channel/key sequences did not continue after restore: channel=%d key=%d", channel.ID, channel.Keys[0].ID)
	}

	updatedName := "matrix-crud-channel-updated"
	updatedChannel, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, Name: &updatedName}, ctx)
	if err != nil {
		t.Fatalf("update channel through operation layer: %v", err)
	}
	if updatedChannel.Name != updatedName {
		t.Fatalf("updated channel name = %q, want %q", updatedChannel.Name, updatedName)
	}
	var storedChannel model.Channel
	if err := db.GetDB().First(&storedChannel, channel.ID).Error; err != nil {
		t.Fatalf("read updated channel: %v", err)
	}
	if storedChannel.Name != updatedName {
		t.Fatalf("stored channel name = %q, want %q", storedChannel.Name, updatedName)
	}

	group := model.Group{
		Name: "matrix-crud-group",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{{
			Type: model.GroupItemTypeChannel, ChannelID: channel.ID,
			ModelName: "matrix-upstream-model", Priority: 1, Weight: 1,
		}},
	}
	if err := GroupCreate(&group, ctx); err != nil {
		t.Fatalf("create group through operation layer: %v", err)
	}
	if group.ID <= 20 || len(group.Items) != 1 || group.Items[0].ID <= 30 {
		t.Fatalf("group/item sequences did not continue after restore: group=%d item=%d", group.ID, group.Items[0].ID)
	}
	priority := 2
	updatedGroup, err := GroupUpdate(&model.GroupUpdateRequest{
		ID: group.ID,
		ItemsToUpdate: []model.GroupItemUpdateRequest{{
			ID: group.Items[0].ID, Priority: &priority,
		}},
	}, ctx)
	if err != nil {
		t.Fatalf("update group through operation layer: %v", err)
	}
	if len(updatedGroup.Items) != 1 || updatedGroup.Items[0].Priority != priority {
		t.Fatalf("updated group item mismatch: %#v", updatedGroup.Items)
	}

	key := model.APIKey{
		Name: "matrix-crud-api-key", APIKey: "sk-matrix-client", Enabled: true,
		MaxCost: 10, SupportedModels: group.Name,
	}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("create API key through operation layer: %v", err)
	}
	if key.ID <= 40 {
		t.Fatalf("API key sequence did not continue after restore: id=%d", key.ID)
	}
	key.Name = "matrix-crud-api-key-updated"
	key.MaxCost = 20
	if err := APIKeyUpdate(&key, ctx); err != nil {
		t.Fatalf("update API key through operation layer: %v", err)
	}
	gotKey, err := APIKeyGetByAPIKey("sk-matrix-client", ctx)
	if err != nil {
		t.Fatalf("read API key through reverse cache: %v", err)
	}
	if gotKey.Name != key.Name || gotKey.MaxCost != 20 {
		t.Fatalf("updated API key mismatch: %#v", gotKey)
	}
	if err := APIKeyDelete(key.ID, ctx); err != nil {
		t.Fatalf("delete API key through operation layer: %v", err)
	}
	var keyCount int64
	if err := db.GetDB().Model(&model.APIKey{}).Where("id = ?", key.ID).Count(&keyCount).Error; err != nil {
		t.Fatalf("count deleted API key: %v", err)
	}
	if keyCount != 0 {
		t.Fatalf("deleted API key still has %d rows", keyCount)
	}
}

func assertDatabaseMatrixConcurrentDeletes(t *testing.T, ctx context.Context) {
	t.Helper()
	const workers = 6
	ids := make([]int, 0, workers)
	for i := 0; i < workers; i++ {
		channel := model.Channel{
			Name:     fmt.Sprintf("matrix-delete-channel-%d", i),
			Type:     llm.APIFormatOpenAIChatCompletion,
			Enabled:  true,
			BaseUrls: []model.BaseUrl{{URL: fmt.Sprintf("https://delete-%d.example.test/v1", i)}},
			Model:    "matrix-delete-model",
			Keys: []model.ChannelKey{{
				Enabled: true, ChannelKey: fmt.Sprintf("sk-matrix-delete-%d", i),
			}},
		}
		if err := ChannelCreate(&channel, ctx); err != nil {
			t.Fatalf("create concurrent-delete channel %d: %v", i, err)
		}
		if err := db.GetDB().Create(&model.StatsChannel{ChannelID: channel.ID}).Error; err != nil {
			t.Fatalf("create concurrent-delete stats %d: %v", i, err)
		}
		ids = append(ids, channel.ID)
	}

	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- ChannelDel(id, ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent channel delete: %v", err)
		}
	}

	for _, id := range ids {
		for _, row := range []struct {
			name  string
			model any
			where string
		}{
			{name: "channels", model: &model.Channel{}, where: "id = ?"},
			{name: "channel_keys", model: &model.ChannelKey{}, where: "channel_id = ?"},
			{name: "stats_channels", model: &model.StatsChannel{}, where: "channel_id = ?"},
		} {
			var count int64
			if err := db.GetDB().Model(row.model).Where(row.where, id).Count(&count).Error; err != nil {
				t.Fatalf("count %s for deleted channel %d: %v", row.name, id, err)
			}
			if count != 0 {
				t.Errorf("%s rows for deleted channel %d = %d, want 0", row.name, id, count)
			}
		}
	}
}

func matrixIncrementalDump(t *testing.T, source *model.DBDump) *model.DBDump {
	t.Helper()
	dump := cloneDBDump(t, source)
	dump.IncludeLogs = false
	dump.RelayLogs = nil
	dump.Channels[0].UUID = "10000000-0000-4000-8000-000000000010"
	dump.Channels[0].Name = "incremental-channel"
	dump.ChannelKeys[0].UUID = "10000000-0000-4000-8000-000000000011"
	dump.ChannelKeys[0].ChannelKey = "sk-incremental-upstream"
	dump.Groups[0].UUID = "10000000-0000-4000-8000-000000000020"
	dump.Groups[0].Name = "incremental-client-model"
	dump.GroupItems[0].UUID = "10000000-0000-4000-8000-000000000030"
	dump.APIKeys[0].UUID = "10000000-0000-4000-8000-000000000040"
	dump.APIKeys[0].Name = "incremental-client"
	dump.APIKeys[0].APIKey = "sk-incremental-client"
	dump.APIKeys[0].SupportedModels = dump.Groups[0].Name
	dump.Relations = &model.DBDumpRelationsV2{
		ChannelKeys: map[string]string{
			dump.ChannelKeys[0].UUID: dump.Channels[0].UUID,
		},
		GroupItems: map[string]model.DBDumpGroupItemRelation{
			dump.GroupItems[0].UUID: {
				GroupUUID:   dump.Groups[0].UUID,
				ChannelUUID: dump.Channels[0].UUID,
			},
		},
	}
	return dump
}

func registerBackupUpdateCallback(t *testing.T, callback func(*gorm.DB)) {
	t.Helper()
	processor := db.GetDB().Callback().Update()
	name := fmt.Sprintf("test:backup-update:%s", strings.ReplaceAll(t.Name(), "/", ":"))
	if err := processor.Before("gorm:update").Register(name, callback); err != nil {
		t.Fatalf("register backup update callback: %v", err)
	}
	t.Cleanup(func() {
		if err := processor.Remove(name); err != nil {
			t.Errorf("remove backup update callback: %v", err)
		}
	})
}

func matrixResetRuntimeState() {
	channelCache.Clear()
	channelKeyCache.Clear()
	channelKeyCacheNeedUpdate.reset()
	groupCache.Clear()
	groupMap.Clear()
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	settingCache.Clear()
	llmModelCache.Clear()
	// The matrix calls InitCache, which populates every statistics field. A new
	// service reliably isolates total/daily/hourly state, dirty sets, pending
	// daily snapshots, and per-channel/API-key caches from other package tests.
	statsService = NewStatsService()
}
