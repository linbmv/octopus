package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func TestChannelKeySaveDBRetainsDirtySnapshotAfterPartialFailure(t *testing.T) {
	setupChannelKeyPersistenceTest(t)
	_, keys := createChannelKeyPersistenceFixture(t, 2)

	first := keys[0]
	first.StatusCode = 401
	first.TotalCost = 1.25
	second := keys[1]
	second.StatusCode = 429
	second.TotalCost = 2.5
	if err := ChannelKeyUpdate(first); err != nil {
		t.Fatalf("update first channel key: %v", err)
	}
	if err := ChannelKeyUpdate(second); err != nil {
		t.Fatalf("update second channel key: %v", err)
	}

	wantErr := errors.New("injected second channel key save failure")
	failEnabled := true
	registerChannelKeyUpdateTestCallback(t, func(tx *gorm.DB, key model.ChannelKey) {
		if failEnabled && key.ID == second.ID {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("AddError() = %v, want injected error", err)
			}
		}
	})

	err := ChannelKeySaveDB(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ChannelKeySaveDB() error = %v, want injected error", err)
	}
	assertDirtyChannelKeyIDs(t, first.ID, second.ID)
	assertPersistedChannelKey(t, first.ID, first)
	assertPersistedChannelKey(t, second.ID, keys[1])

	failEnabled = false
	if err := ChannelKeySaveDB(context.Background()); err != nil {
		t.Fatalf("ChannelKeySaveDB() retry error = %v", err)
	}
	assertDirtyChannelKeyIDs(t)
	assertPersistedChannelKey(t, first.ID, first)
	assertPersistedChannelKey(t, second.ID, second)
}

func TestChannelKeySaveDBPreservesUpdateMadeDuringSave(t *testing.T) {
	setupChannelKeyPersistenceTest(t)
	_, keys := createChannelKeyPersistenceFixture(t, 1)

	pending := keys[0]
	pending.StatusCode = 401
	pending.TotalCost = 1.25
	if err := ChannelKeyUpdate(pending); err != nil {
		t.Fatalf("set pending channel key update: %v", err)
	}

	saveStarted := make(chan struct{})
	allowSave := make(chan struct{})
	var blockFirstSave sync.Once
	registerChannelKeyUpdateTestCallback(t, func(tx *gorm.DB, key model.ChannelKey) {
		if key.ID != pending.ID {
			return
		}
		blockFirstSave.Do(func() {
			close(saveStarted)
			select {
			case <-allowSave:
			case <-time.After(2 * time.Second):
				timeoutErr := errors.New("timed out waiting to release channel key save")
				if err := tx.AddError(timeoutErr); !errors.Is(err, timeoutErr) {
					t.Errorf("AddError() = %v, want timeout error", err)
				}
			}
		})
	})

	saveResult := make(chan error, 1)
	go func() {
		saveResult <- ChannelKeySaveDB(context.Background())
	}()
	select {
	case <-saveStarted:
	case <-time.After(2 * time.Second):
		close(allowSave)
		t.Fatal("ChannelKeySaveDB() did not reach the persistence callback")
	}

	newer := pending
	newer.StatusCode = 429
	newer.TotalCost = 9.5
	if err := ChannelKeyUpdate(newer); err != nil {
		close(allowSave)
		t.Fatalf("update channel key during save: %v", err)
	}
	close(allowSave)
	select {
	case err := <-saveResult:
		if err != nil {
			t.Fatalf("ChannelKeySaveDB() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ChannelKeySaveDB() did not finish")
	}

	assertPersistedChannelKey(t, pending.ID, pending)
	assertDirtyChannelKeyIDs(t, pending.ID)
	if err := ChannelKeySaveDB(context.Background()); err != nil {
		t.Fatalf("ChannelKeySaveDB() retry error = %v", err)
	}
	assertPersistedChannelKey(t, newer.ID, newer)
	assertDirtyChannelKeyIDs(t)
}

func TestSaveCacheReturnsChannelKeyPersistenceError(t *testing.T) {
	setupChannelKeyPersistenceTest(t)
	_, keys := createChannelKeyPersistenceFixture(t, 1)
	pending := keys[0]
	pending.StatusCode = 503
	if err := ChannelKeyUpdate(pending); err != nil {
		t.Fatalf("set pending channel key update: %v", err)
	}

	oldStatsService := statsService
	statsService = NewStatsService()
	t.Cleanup(func() { statsService = oldStatsService })
	wantErr := errors.New("injected shutdown channel key save failure")
	registerChannelKeyUpdateTestCallback(t, func(tx *gorm.DB, key model.ChannelKey) {
		if key.ID == pending.ID {
			if err := tx.AddError(wantErr); !errors.Is(err, wantErr) {
				t.Errorf("AddError() = %v, want injected error", err)
			}
		}
	})

	err := SaveCache()
	if !errors.Is(err, wantErr) {
		t.Fatalf("SaveCache() error = %v, want channel key persistence error", err)
	}
	assertDirtyChannelKeyIDs(t, pending.ID)
}

func setupChannelKeyPersistenceTest(t *testing.T) {
	t.Helper()
	initTestDB(t)
	reset := func() {
		channelKeyCache.Clear()
		channelKeyCacheNeedUpdate.reset()
	}
	reset()
	t.Cleanup(reset)
}

func createChannelKeyPersistenceFixture(t *testing.T, keyCount int) (model.Channel, []model.ChannelKey) {
	t.Helper()
	channel := model.Channel{Name: "channel-key-persistence", Enabled: true}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	keys := make([]model.ChannelKey, keyCount)
	for i := range keys {
		keys[i] = model.ChannelKey{
			ChannelID:  channel.ID,
			Enabled:    true,
			ChannelKey: fmt.Sprintf("upstream-key-%d", i+1),
		}
	}
	if err := db.GetDB().Create(&keys).Error; err != nil {
		t.Fatalf("create channel keys: %v", err)
	}
	channel.Keys = append([]model.ChannelKey(nil), keys...)
	channelCache.Set(channel.ID, channel)
	for _, key := range keys {
		channelKeyCache.Set(key.ID, key)
	}
	return channel, keys
}

func registerChannelKeyUpdateTestCallback(t *testing.T, callback func(*gorm.DB, model.ChannelKey)) {
	t.Helper()
	processor := db.GetDB().Callback().Update()
	name := fmt.Sprintf("test:channel-key-persistence:%s", strings.ReplaceAll(t.Name(), "/", ":"))
	if err := processor.Before("gorm:update").Register(name, func(tx *gorm.DB) {
		key, ok := tx.Statement.Dest.(*model.ChannelKey)
		if ok && key != nil {
			callback(tx, *key)
		}
	}); err != nil {
		t.Fatalf("register channel key update callback: %v", err)
	}
	t.Cleanup(func() {
		if err := processor.Remove(name); err != nil {
			t.Errorf("remove channel key update callback: %v", err)
		}
	})
}

func assertDirtyChannelKeyIDs(t *testing.T, want ...int) {
	t.Helper()
	got := dirtyIDs(channelKeyCacheNeedUpdate.snapshot())
	if len(got) != len(want) {
		t.Fatalf("dirty channel key IDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirty channel key IDs = %v, want %v", got, want)
		}
	}
}

func assertPersistedChannelKey(t *testing.T, id int, want model.ChannelKey) {
	t.Helper()
	var got model.ChannelKey
	if err := db.GetDB().First(&got, id).Error; err != nil {
		t.Fatalf("get persisted channel key %d: %v", id, err)
	}
	if got.StatusCode != want.StatusCode || got.TotalCost != want.TotalCost {
		t.Fatalf("persisted channel key %d = status %d, cost %v; want status %d, cost %v",
			id, got.StatusCode, got.TotalCost, want.StatusCode, want.TotalCost)
	}
}
