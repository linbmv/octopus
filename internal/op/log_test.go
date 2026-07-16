package op

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func TestRelayLogServiceTokens(t *testing.T) {
	service := NewRelayLogService()

	token, err := service.StreamTokenCreate()
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if token == "" {
		t.Fatal("expected token")
	}
	if !service.StreamTokenConsume(token) {
		t.Fatal("expected token to be consumed")
	}
	if service.StreamTokenConsume(token) {
		t.Fatal("one-time token was consumed twice")
	}
}

func TestRelayLogStreamTokenExpiresAndSweeps(t *testing.T) {
	service := NewRelayLogService()

	token, err := service.StreamTokenCreate()
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	// 人为过期：Consume 应拒绝并顺带删除
	service.streamTokensMu.Lock()
	service.streamTokens[token] = time.Now().Add(-time.Second)
	service.streamTokensMu.Unlock()
	if service.StreamTokenConsume(token) {
		t.Fatal("过期 token 必须校验失败")
	}
	service.streamTokensMu.Lock()
	_, stillThere := service.streamTokens[token]
	service.streamTokensMu.Unlock()
	if stillThere {
		t.Fatal("过期 token 应在校验时被删除")
	}

	// 未消费的过期 token 应在下一次 Create 时被清扫
	service.streamTokensMu.Lock()
	service.streamTokens["stale-unconsumed"] = time.Now().Add(-time.Second)
	service.streamTokensMu.Unlock()
	if _, err := service.StreamTokenCreate(); err != nil {
		t.Fatalf("create token: %v", err)
	}
	service.streamTokensMu.Lock()
	_, staleThere := service.streamTokens["stale-unconsumed"]
	service.streamTokensMu.Unlock()
	if staleThere {
		t.Fatal("过期未消费 token 应在签发新 token 时被清扫")
	}
}

func TestRelayLogStreamTokenConcurrentConsumeHasSingleWinner(t *testing.T) {
	service := NewRelayLogService()
	token, err := service.StreamTokenCreate()
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	const contenders = 32
	var winners atomic.Int64
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if service.StreamTokenConsume(token) {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("concurrent token winners = %d, want 1", got)
	}
}

func TestRelayLogStreamTokenMapHasHardLimitAndRecoversAfterExpiry(t *testing.T) {
	service := NewRelayLogService()
	for i := 0; i < relayLogStreamTokenMaxEntries; i++ {
		if _, err := service.StreamTokenCreate(); err != nil {
			t.Fatalf("StreamTokenCreate(%d) error = %v", i, err)
		}
	}
	if _, err := service.StreamTokenCreate(); err == nil || !strings.Contains(err.Error(), "too many unconsumed") {
		t.Fatalf("StreamTokenCreate() at hard limit error = %v", err)
	}

	service.streamTokensMu.Lock()
	for token := range service.streamTokens {
		service.streamTokens[token] = time.Now().Add(-time.Second)
		break
	}
	service.streamTokensMu.Unlock()
	if _, err := service.StreamTokenCreate(); err != nil {
		t.Fatalf("StreamTokenCreate() after expiry sweep error = %v", err)
	}
}

func TestRelayLogCursorPaginationIsStableAcrossConcurrentInsert(t *testing.T) {
	setupRelayLogPersistenceTest(t)
	ctx := context.Background()
	service := NewRelayLogService()

	dbLogs := []model.RelayLog{
		{ID: 1, Time: 1}, {ID: 2, Time: 2}, {ID: 3, Time: 3},
		{ID: 4, Time: 4}, {ID: 5, Time: 5},
	}
	if err := db.GetDB().Create(&dbLogs).Error; err != nil {
		t.Fatalf("seed DB logs: %v", err)
	}
	// ID 5 overlaps a just-flushed DB row; ID 6 exists only in memory.
	service.cache = []model.RelayLog{{ID: 5, Time: 5}, {ID: 6, Time: 6}}

	first, err := service.ListCursor(ctx, nil, nil, 0, 2)
	if err != nil {
		t.Fatalf("first cursor page: %v", err)
	}
	if got := relayLogIDs(first.Items); !slices.Equal(got, []int64{6, 5}) {
		t.Fatalf("first IDs = %v, want [6 5]", got)
	}
	if !first.HasMore || first.NextCursor != 5 {
		t.Fatalf("first cursor metadata = %+v", first)
	}

	// A new newest row must not shift the second page as offset pagination does.
	service.cache = append(service.cache, model.RelayLog{ID: 7, Time: 7})
	second, err := service.ListCursor(ctx, nil, nil, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second cursor page: %v", err)
	}
	if got := relayLogIDs(second.Items); !slices.Equal(got, []int64{4, 3}) {
		t.Fatalf("second IDs = %v, want [4 3]", got)
	}
}

func TestRelayLogListAfterDeduplicatesAndSignalsGap(t *testing.T) {
	setupRelayLogPersistenceTest(t)
	service := NewRelayLogService()
	dbLogs := []model.RelayLog{{ID: 1}, {ID: 2}, {ID: 3}}
	if err := db.GetDB().Create(&dbLogs).Error; err != nil {
		t.Fatalf("seed DB logs: %v", err)
	}
	service.cache = []model.RelayLog{{ID: 3}, {ID: 4}, {ID: 5}}

	items, truncated, err := service.ListAfter(context.Background(), 1, 3)
	if err != nil {
		t.Fatalf("ListAfter() error = %v", err)
	}
	if !truncated {
		t.Fatal("ListAfter() should signal a replay gap")
	}
	if got := relayLogIDs(items); !slices.Equal(got, []int64{3, 4, 5}) {
		t.Fatalf("replayed IDs = %v, want newest ascending [3 4 5]", got)
	}
}

func relayLogIDs(logs []model.RelayLog) []int64 {
	ids := make([]int64, len(logs))
	for i := range logs {
		ids[i] = logs[i].ID
	}
	return ids
}

func TestRelayLogServiceListIncludesMetadataLogsNewestFirst(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("refresh settings: %v", err)
	}

	service := NewRelayLogService()
	success := model.RelayLog{
		ID:               1,
		Time:             100,
		RequestModelName: "gpt-test",
		ChannelName:      "ok-channel",
		ActualModelName:  "gpt-test",
		ResponseContent:  `{"ok":true}`,
	}
	failure := model.RelayLog{
		ID:               2,
		Time:             100,
		RequestModelName: "gpt-test",
		ChannelName:      "bad-channel",
		ActualModelName:  "gpt-test",
		Error:            "upstream failed",
	}

	if err := service.Add(ctx, success); err != nil {
		t.Fatalf("add success log: %v", err)
	}
	if err := service.Add(ctx, failure); err != nil {
		t.Fatalf("add failure log: %v", err)
	}

	logs, err := service.List(ctx, nil, nil, 1, 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("log count = %d, want 2: %#v", len(logs), logs)
	}
	if logs[0].Error != failure.Error || logs[1].ResponseContent != "" {
		t.Fatalf("logs not sorted by newest id within same second: %#v", logs)
	}
	if logs[1].Error != "" || logs[1].RequestModelName != success.RequestModelName {
		t.Fatalf("success metadata log missing from list or classified as error: %#v", logs[1])
	}
}

func TestRelayLogServiceSubscription(t *testing.T) {
	service := NewRelayLogService()
	ch := service.Subscribe()

	service.notifySubscribers(model.RelayLog{ID: 1})

	select {
	case got := <-ch:
		if got.ID != 1 {
			t.Fatalf("unexpected log id: %d", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay log notification")
	}

	service.Unsubscribe(ch)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected unsubscribe to close channel")
		}
	default:
		t.Fatal("expected closed channel")
	}
}

func TestRelayLogNotificationQueueIsBoundedAndObservable(t *testing.T) {
	service := NewRelayLogService()
	total := relayLogNotifyQueueSize * 4

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			service.enqueueNotification(model.RelayLog{ID: int64(id + 1)})
		}(i)
	}
	wg.Wait()

	queued := len(service.notifyQueue)
	dropped := service.notifyQueueDropped.Load()
	if queued > relayLogNotifyQueueSize {
		t.Fatalf("notification queue length = %d, hard limit %d", queued, relayLogNotifyQueueSize)
	}
	if uint64(queued)+dropped != uint64(total) {
		t.Fatalf("queued + dropped = %d + %d, want %d", queued, dropped, total)
	}
}

func TestRelayLogSlowSubscriberDropsAreCounted(t *testing.T) {
	service := NewRelayLogService()
	subscriber := service.Subscribe()
	defer service.Unsubscribe(subscriber)

	total := cap(subscriber) + 3
	for i := 0; i < total; i++ {
		service.notifySubscribers(model.RelayLog{ID: int64(i + 1)})
	}
	if got := service.subscriberDropped.Load(); got != 3 {
		t.Fatalf("slow subscriber dropped count = %d, want 3", got)
	}
}

func TestRelayLogWorkersDrainNotificationQueueOnStop(t *testing.T) {
	service := NewRelayLogService()
	subscriber := service.Subscribe()
	defer service.Unsubscribe(subscriber)

	const total = 5
	for i := 0; i < total; i++ {
		service.enqueueNotification(model.RelayLog{ID: int64(i + 1)})
	}
	service.StartFlushWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.StopFlushWorker(ctx); err != nil {
		t.Fatalf("StopFlushWorker() error = %v", err)
	}
	if queued := len(service.notifyQueue); queued != 0 {
		t.Fatalf("notification queue length after stop = %d, want 0", queued)
	}
	for wantID := int64(1); wantID <= total; wantID++ {
		select {
		case got := <-subscriber:
			if got.ID != wantID {
				t.Fatalf("notification ID = %d, want %d", got.ID, wantID)
			}
		default:
			t.Fatalf("missing drained notification %d", wantID)
		}
	}
}

func TestRelayLogCacheIsBoundedAcrossDBFailuresAndRecovers(t *testing.T) {
	setupRelayLogPersistenceTest(t)
	service := NewRelayLogService()
	wantErr := errors.New("injected relay log flush failure")
	failFlush := true
	registerRelayLogCreateTestCallback(t, func(tx *gorm.DB) {
		if failFlush {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("inject relay log flush failure: %v", err)
			}
		}
	})

	total := relayLogCacheHardLimit + 25
	for i := 0; i < total; i++ {
		if err := service.Add(context.Background(), model.RelayLog{Time: int64(i)}); err != nil {
			t.Fatalf("Add(%d) error = %v", i, err)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := service.flushToDB(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("flush attempt %d error = %v, want injected error", attempt+1, err)
		}
	}

	service.cacheMu.Lock()
	retained := append([]model.RelayLog(nil), service.cache...)
	service.cacheMu.Unlock()
	if len(retained) >= relayLogCacheHardLimit {
		t.Fatalf("relay log cache length = %d, want below hard limit %d", len(retained), relayLogCacheHardLimit)
	}
	wantDropped := uint64(total - len(retained))
	if got := service.cacheDropped.Load(); got != wantDropped {
		t.Fatalf("cache evicted count = %d, want %d", got, wantDropped)
	}
	if got := service.flushFailures.Load(); got != 3 {
		t.Fatalf("flush failure count = %d, want 3", got)
	}
	if len(retained) == 0 || retained[0].Time != int64(total-len(retained)) {
		t.Fatalf("cache did not retain the newest logs: first=%#v len=%d", retained, len(retained))
	}

	failFlush = false
	if err := service.flushToDB(context.Background()); err != nil {
		t.Fatalf("flush after DB recovery error = %v", err)
	}
	service.cacheMu.Lock()
	cacheLen := len(service.cache)
	service.cacheMu.Unlock()
	if cacheLen != 0 {
		t.Fatalf("cache length after recovery flush = %d, want 0", cacheLen)
	}
	assertRelayLogDBCount(t, int64(len(retained)))

	if err := service.Add(context.Background(), model.RelayLog{Time: int64(total)}); err != nil {
		t.Fatalf("Add() after recovery error = %v", err)
	}
	if err := service.flushToDB(context.Background()); err != nil {
		t.Fatalf("second recovery flush error = %v", err)
	}
	assertRelayLogDBCount(t, int64(len(retained)+1))
}

func setupRelayLogPersistenceTest(t *testing.T) {
	t.Helper()
	initTestDB(t)
	savedSettings := settingCache.GetAll()
	settingCache.Clear()
	settingCache.Set(model.SettingKeyRelayLogKeepEnabled, "true")
	settingCache.Set(model.SettingKeyRelayLogKeepPeriod, "0")
	settingCache.Set(model.SettingKeyRelayLogContentMode, string(model.RelayLogContentModeMetadata))
	t.Cleanup(func() {
		settingCache.Clear()
		for key, value := range savedSettings {
			settingCache.Set(key, value)
		}
	})
}

func registerRelayLogCreateTestCallback(t *testing.T, callback func(*gorm.DB)) {
	t.Helper()
	processor := db.GetDB().Callback().Create()
	name := fmt.Sprintf("test:relay-log-persistence:%s", strings.ReplaceAll(t.Name(), "/", ":"))
	if err := processor.Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "relay_logs" {
			callback(tx)
		}
	}); err != nil {
		t.Fatalf("register relay log create callback: %v", err)
	}
	t.Cleanup(func() {
		if err := processor.Remove(name); err != nil {
			t.Errorf("remove relay log create callback: %v", err)
		}
	})
}

func assertRelayLogDBCount(t *testing.T, want int64) {
	t.Helper()
	var got int64
	if err := db.GetDB().Model(&model.RelayLog{}).Count(&got).Error; err != nil {
		t.Fatalf("count relay logs: %v", err)
	}
	if got != want {
		t.Fatalf("relay log DB count = %d, want %d", got, want)
	}
}
