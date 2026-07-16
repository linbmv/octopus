package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func TestChannelDelReturnsBeginError(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	channel := createDeleteTestChannel(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ChannelDel(channel.ID, ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ChannelDel() error = %v, want context canceled", err)
	}
	if !strings.Contains(err.Error(), "failed to begin transaction") {
		t.Fatalf("ChannelDel() error = %q, want begin transaction context", err)
	}
	assertDeleteTestRowCount(t, &model.Channel{}, "id = ?", channel.ID, 1)
	if _, ok := channelCache.Get(channel.ID); !ok {
		t.Fatal("channel cache changed after Begin failed")
	}
}

func TestChannelDelRollsBackAndPropagatesTransactionError(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	graph := createDeleteTestChannelGraph(t)
	wantErr := errors.New("injected channel stats delete failure")
	registerDeleteTestCallback(t, func(tx *gorm.DB) {
		if deleteTestTable(tx) == "stats_channels" {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("inject channel stats delete failure: %v", err)
			}
		}
	})

	err := ChannelDel(graph.channel.ID, context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ChannelDel() error = %v, want injected error", err)
	}
	assertChannelDeleteGraphPresent(t, graph)
}

func TestChannelDelRollsBackAndRepanics(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	graph := createDeleteTestChannelGraph(t)
	wantPanic := errors.New("injected channel key delete panic")
	registerDeleteTestCallback(t, func(tx *gorm.DB) {
		if deleteTestTable(tx) == "channel_keys" {
			panic(wantPanic)
		}
	})

	var recovered interface{}
	func() {
		defer func() { recovered = recover() }()
		_ = ChannelDel(graph.channel.ID, context.Background())
	}()
	if recovered != wantPanic {
		t.Fatalf("ChannelDel() recovered = %v, want %v", recovered, wantPanic)
	}
	assertChannelDeleteGraphPresent(t, graph)
}

func TestAPIKeyDeleteDeletesStatsAndAllCacheIndexes(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	key := createDeleteTestAPIKey(t)

	if err := APIKeyDelete(key.ID, context.Background()); err != nil {
		t.Fatalf("APIKeyDelete() error = %v", err)
	}
	assertDeleteTestRowCount(t, &model.APIKey{}, "id = ?", key.ID, 0)
	assertDeleteTestRowCount(t, &model.StatsAPIKey{}, "api_key_id = ?", key.ID, 0)
	if _, ok := apiKeyCache.Get(key.ID); ok {
		t.Fatal("API key ID cache entry survived deletion")
	}
	if _, ok := apiKeyIDMap.Get(key.APIKey); ok {
		t.Fatal("API key reverse cache entry survived deletion")
	}
	if _, err := APIKeyGet(key.ID, context.Background()); err == nil {
		t.Fatal("deleted API key is still available by ID")
	}
	if _, err := APIKeyGetByAPIKey(key.APIKey, context.Background()); err == nil {
		t.Fatal("deleted API key is still available for authentication lookup")
	}
	if _, ok := statsService.apiKeys.Get(key.ID); ok {
		t.Fatal("API key stats cache entry survived deletion")
	}
	if dirty := statsService.takeDirtyAPIKeys(); len(dirty) != 0 {
		t.Fatalf("dirty API key stats survived deletion: %v", dirty)
	}
}

func TestAPIKeyDeleteRollsBackWhenStatsDeleteFails(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	key := createDeleteTestAPIKey(t)
	wantErr := errors.New("injected API key stats delete failure")
	registerDeleteTestCallback(t, func(tx *gorm.DB) {
		if deleteTestTable(tx) == "stats_api_keys" {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("inject API key stats delete failure: %v", err)
			}
		}
	})

	err := APIKeyDelete(key.ID, context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("APIKeyDelete() error = %v, want injected error", err)
	}
	assertAPIKeyDeleteStatePresent(t, key)
}

func TestAPIKeyDeleteReportsDatabaseErrorBeforeRowsAffected(t *testing.T) {
	initTestDB(t)
	resetDeleteTestCaches(t)
	key := createDeleteTestAPIKey(t)
	wantErr := errors.New("injected API key delete failure")
	registerDeleteTestCallback(t, func(tx *gorm.DB) {
		if deleteTestTable(tx) == "api_keys" {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("inject API key delete failure: %v", err)
			}
		}
	})

	err := APIKeyDelete(key.ID, context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("APIKeyDelete() error = %v, want database error instead of not found", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("APIKeyDelete() masked database error as not found: %v", err)
	}
	assertAPIKeyDeleteStatePresent(t, key)
}

type deleteTestChannelGraph struct {
	channel   model.Channel
	key       model.ChannelKey
	groupItem model.GroupItem
	stats     model.StatsChannel
}

func createDeleteTestChannel(t *testing.T) model.Channel {
	t.Helper()
	channel := model.Channel{Name: "delete-test-channel", Enabled: true}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelCache.Set(channel.ID, channel)
	return channel
}

func createDeleteTestChannelGraph(t *testing.T) deleteTestChannelGraph {
	t.Helper()
	channel := createDeleteTestChannel(t)
	key := model.ChannelKey{ChannelID: channel.ID, Enabled: true, ChannelKey: "upstream-key"}
	if err := db.GetDB().Create(&key).Error; err != nil {
		t.Fatalf("create channel key: %v", err)
	}
	group := model.Group{Name: "delete-test-group", Enabled: true, Mode: model.GroupModeRoundRobin}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	item := model.GroupItem{
		GroupID:   group.ID,
		Type:      model.GroupItemTypeChannel,
		ChannelID: channel.ID,
		ModelName: "delete-test-model",
	}
	if err := db.GetDB().Create(&item).Error; err != nil {
		t.Fatalf("create group item: %v", err)
	}
	stats := model.StatsChannel{ChannelID: channel.ID, StatsMetrics: model.StatsMetrics{RequestSuccess: 1}}
	if err := db.GetDB().Create(&stats).Error; err != nil {
		t.Fatalf("create channel stats: %v", err)
	}

	channel.Keys = []model.ChannelKey{key}
	channelCache.Set(channel.ID, channel)
	channelKeyCache.Set(key.ID, key)
	statsService.channels.Set(channel.ID, stats)
	statsService.dirtyChannels.mark(channel.ID)
	return deleteTestChannelGraph{channel: channel, key: key, groupItem: item, stats: stats}
}

func assertChannelDeleteGraphPresent(t *testing.T, graph deleteTestChannelGraph) {
	t.Helper()
	assertDeleteTestRowCount(t, &model.Channel{}, "id = ?", graph.channel.ID, 1)
	assertDeleteTestRowCount(t, &model.ChannelKey{}, "id = ?", graph.key.ID, 1)
	assertDeleteTestRowCount(t, &model.GroupItem{}, "id = ?", graph.groupItem.ID, 1)
	assertDeleteTestRowCount(t, &model.StatsChannel{}, "channel_id = ?", graph.stats.ChannelID, 1)
	if _, ok := channelCache.Get(graph.channel.ID); !ok {
		t.Fatal("channel cache changed before transaction committed")
	}
	if _, ok := channelKeyCache.Get(graph.key.ID); !ok {
		t.Fatal("channel key cache changed before transaction committed")
	}
}

func createDeleteTestAPIKey(t *testing.T) model.APIKey {
	t.Helper()
	key := model.APIKey{Name: "delete-test-api-key", APIKey: "sk-delete-test", Enabled: true}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("create API key: %v", err)
	}
	stats := model.StatsAPIKey{APIKeyID: key.ID, StatsMetrics: model.StatsMetrics{RequestSuccess: 1}}
	if err := db.GetDB().Create(&stats).Error; err != nil {
		t.Fatalf("create API key stats: %v", err)
	}
	statsService.apiKeys.Set(key.ID, stats)
	statsService.dirtyAPIKeys.mark(key.ID)
	return key
}

func assertAPIKeyDeleteStatePresent(t *testing.T, key model.APIKey) {
	t.Helper()
	assertDeleteTestRowCount(t, &model.APIKey{}, "id = ?", key.ID, 1)
	assertDeleteTestRowCount(t, &model.StatsAPIKey{}, "api_key_id = ?", key.ID, 1)
	if _, ok := apiKeyCache.Get(key.ID); !ok {
		t.Fatal("API key ID cache changed after transaction rolled back")
	}
	if id, ok := apiKeyIDMap.Get(key.APIKey); !ok || id != key.ID {
		t.Fatalf("API key reverse cache = (%d, %v), want (%d, true)", id, ok, key.ID)
	}
	if _, ok := statsService.apiKeys.Get(key.ID); !ok {
		t.Fatal("API key stats cache changed after transaction rolled back")
	}
}

func assertDeleteTestRowCount(t *testing.T, value interface{}, query string, arg interface{}, want int64) {
	t.Helper()
	var count int64
	if err := db.GetDB().Model(value).Where(query, arg).Count(&count).Error; err != nil {
		t.Fatalf("count %T rows: %v", value, err)
	}
	if count != want {
		t.Fatalf("count %T rows = %d, want %d", value, count, want)
	}
}

func registerDeleteTestCallback(t *testing.T, callback func(*gorm.DB)) {
	t.Helper()
	processor := db.GetDB().Callback().Delete()
	name := fmt.Sprintf("test:delete-consistency:%s", strings.ReplaceAll(t.Name(), "/", ":"))
	if err := processor.Before("gorm:delete").Register(name, callback); err != nil {
		t.Fatalf("register delete callback: %v", err)
	}
	t.Cleanup(func() {
		if err := processor.Remove(name); err != nil {
			t.Errorf("remove delete callback: %v", err)
		}
	})
}

func deleteTestTable(tx *gorm.DB) string {
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return tx.Statement.Table
}

func resetDeleteTestCaches(t *testing.T) {
	t.Helper()
	reset := func() {
		channelKeyCache.Clear()
		channelKeyCacheNeedUpdate.reset()
		apiKeyCache.Clear()
		apiKeyIDMap.Clear()
		statsService.channels.Clear()
		statsService.dirtyChannels.reset()
		statsService.apiKeys.Clear()
		statsService.dirtyAPIKeys.reset()
	}
	reset()
	t.Cleanup(reset)
}
