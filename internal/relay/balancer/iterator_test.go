package balancer

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func clearSessions() {
	globalSession.Range(func(key, value any) bool {
		globalSession.Delete(key)
		return true
	})
}

func TestNewIteratorMovesStickyChannelAndKeepsStickyKeyID(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 7
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 101
	)

	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-a")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-a", Priority: 2},
			{ChannelID: 2, ModelName: "upstream-b", Priority: 1},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if got := iter.Item().ChannelID; got != channelID {
		t.Fatalf("sticky channel ID = %d, want %d", got, channelID)
	}
	if !iter.IsSticky() {
		t.Fatal("IsSticky() = false, want true")
	}
	if got := iter.StickyKeyID(); got != channelKeyID {
		t.Errorf("StickyKeyID() = %d, want %d", got, channelKeyID)
	}
}

func TestStickyKeyIDReturnsZeroOutsideStickyCandidate(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 8
		requestModel = "octopus-model"
	)

	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-a", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-b", Priority: 2},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if iter.IsSticky() {
		t.Fatal("IsSticky() = true, want false")
	}
	if got := iter.StickyKeyID(); got != 0 {
		t.Errorf("StickyKeyID() = %d, want 0", got)
	}
}

func TestNewIteratorSkipsStickyWhenActualModelDiffers(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 9
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 101
	)

	// 上次成功用的是 upstream-a；本次该渠道候选只有 upstream-b（同渠道不同实际模型）。
	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-a")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-b", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-c", Priority: 2},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	// actual model 不一致，不应复用 sticky，避免 prompt cache miss。
	if iter.IsSticky() {
		t.Fatal("IsSticky() = true, want false (actual model differs)")
	}
	if got := iter.StickyKeyID(); got != 0 {
		t.Errorf("StickyKeyID() = %d, want 0", got)
	}
}

func TestNewIteratorMatchesStickyByActualModelAmongMultipleItems(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 10
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 202
	)

	// 同一渠道在分组内服务多个实际模型；sticky 记录的是 upstream-b，应精准命中该 item。
	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-b")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 2, ModelName: "upstream-x", Priority: 1},
			{ChannelID: 1, ModelName: "upstream-a", Priority: 2},
			{ChannelID: 1, ModelName: "upstream-b", Priority: 3},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if !iter.IsSticky() {
		t.Fatal("IsSticky() = false, want true")
	}
	item := iter.Item()
	if item.ChannelID != channelID || item.ModelName != "upstream-b" {
		t.Fatalf("sticky item = (channel %d, model %s), want (channel %d, model upstream-b)", item.ChannelID, item.ModelName, channelID)
	}
	if got := iter.StickyKeyID(); got != channelKeyID {
		t.Errorf("StickyKeyID() = %d, want %d", got, channelKeyID)
	}
}

func TestSetStickyStoresActualModel(t *testing.T) {
	clearSessions()
	defer clearSessions()

	SetSticky(11, "octopus-model", 3, 303, "upstream-z")
	entry := GetSticky(11, "octopus-model", 60*time.Second)
	if entry == nil {
		t.Fatal("GetSticky() = nil, want entry")
	}
	if entry.ModelName != "upstream-z" {
		t.Fatalf("entry.ModelName = %q, want upstream-z", entry.ModelName)
	}
	if entry.ChannelID != 3 || entry.ChannelKeyID != 303 {
		t.Fatalf("entry = (channel %d, key %d), want (3, 303)", entry.ChannelID, entry.ChannelKeyID)
	}
}
