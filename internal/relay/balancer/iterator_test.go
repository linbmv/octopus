package balancer

import (
	"testing"

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

	SetSticky(apiKeyID, requestModel, channelID, channelKeyID)
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
