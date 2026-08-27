package op

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestNestedGroupsAndTemporaryToggles(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "octopus-test.db")
	if err := db.InitDB("sqlite", databasePath, false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	channel := model.Channel{
		Name:    "test-channel",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		Models: []model.ChannelModel{{
			Name:   "upstream-model",
			Source: model.ChannelModelSourceManual,
		}},
	}
	if err := db.GetDB().WithContext(ctx).Create(&channel).Error; err != nil {
		t.Fatalf("create channel error = %v", err)
	}
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatalf("channelRefreshCache() error = %v", err)
	}
	if err := groupRefreshCache(ctx); err != nil {
		t.Fatalf("groupRefreshCache() error = %v", err)
	}
	channelModelID := channel.Models[0].ID

	leaf := model.Group{
		Name: "leaf",
		Mode: model.GroupModeManual,
		Items: []model.GroupItem{{
			Type:           model.GroupItemTypeChannelModel,
			ChannelModelID: &channelModelID,
			Priority:       1,
		}},
	}
	if err := GroupCreate(&leaf, ctx); err != nil {
		t.Fatalf("GroupCreate(leaf) error = %v", err)
	}
	if !leaf.Enabled {
		t.Fatal("new group must default to enabled")
	}
	leafItemID := leaf.Items[0].ID
	if _, err := GroupActiveItemUpdate(leaf.ID, &model.GroupActiveItemUpdateRequest{ItemID: &leafItemID}, ctx); err != nil {
		t.Fatalf("activate leaf item error = %v", err)
	}

	parent := model.Group{
		Name: "parent",
		Mode: model.GroupModeManual,
		Items: []model.GroupItem{{
			Type:          model.GroupItemTypeGroup,
			TargetGroupID: &leaf.ID,
			Priority:      1,
		}},
	}
	if err := GroupCreate(&parent, ctx); err != nil {
		t.Fatalf("GroupCreate(parent) error = %v", err)
	}
	parentItemID := parent.Items[0].ID
	targetDisabled := false
	if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: leaf.ID, Enabled: &targetDisabled}, ctx); err != nil {
		t.Fatalf("disable nested target error = %v", err)
	}
	if _, err := GroupActiveItemUpdate(parent.ID, &model.GroupActiveItemUpdateRequest{ItemID: &parentItemID}, ctx); err == nil {
		t.Fatal("disabled nested target was accepted as active item")
	}
	targetEnabled := true
	if _, err := GroupUpdate(&model.GroupUpdateRequest{ID: leaf.ID, Enabled: &targetEnabled}, ctx); err != nil {
		t.Fatalf("enable nested target error = %v", err)
	}
	if _, err := GroupActiveItemUpdate(parent.ID, &model.GroupActiveItemUpdateRequest{ItemID: &parentItemID}, ctx); err != nil {
		t.Fatalf("activate nested item error = %v", err)
	}

	disabled := false
	updated, err := GroupUpdate(&model.GroupUpdateRequest{ID: parent.ID, Enabled: &disabled}, ctx)
	if err != nil {
		t.Fatalf("disable group error = %v", err)
	}
	if updated.Enabled {
		t.Fatal("group remained enabled")
	}
	if containsString(GroupListModel(), parent.Name) {
		t.Fatal("disabled group is still exposed in model list")
	}
	if _, err := GroupGetEnabledByName(parent.Name); err == nil {
		t.Fatal("disabled group remained routable")
	}

	enabled := true
	memberDisabled := true
	updated, err = GroupUpdate(&model.GroupUpdateRequest{
		ID:      parent.ID,
		Enabled: &enabled,
		ItemsToUpdate: []model.GroupItemUpdateRequest{{
			ID:       parentItemID,
			Disabled: &memberDisabled,
		}},
	}, ctx)
	if err != nil {
		t.Fatalf("toggle group and member error = %v", err)
	}
	if !updated.Enabled || !updated.Items[0].Disabled {
		t.Fatalf("unexpected toggle state: enabled=%v disabled=%v", updated.Enabled, updated.Items[0].Disabled)
	}
	if _, err := GroupActiveItemUpdate(parent.ID, &model.GroupActiveItemUpdateRequest{ItemID: &parentItemID}, ctx); err == nil {
		t.Fatal("disabled member was accepted as active item")
	}

	// 重新启用成员后尝试 parent -> parent，自引用必须在事务内被拒绝且不留下脏成员。
	memberDisabled = false
	if _, err := GroupUpdate(&model.GroupUpdateRequest{
		ID: parent.ID,
		ItemsToUpdate: []model.GroupItemUpdateRequest{{
			ID:       parentItemID,
			Disabled: &memberDisabled,
		}},
		ItemsToAdd: []model.GroupItemAddRequest{{
			Type:          model.GroupItemTypeGroup,
			TargetGroupID: parent.ID,
			Priority:      2,
		}},
	}, ctx); err == nil || !strings.Contains(err.Error(), "circular") {
		t.Fatalf("self reference error = %v, want circular reference", err)
	}
	parentAfterRollback, err := GroupGetByID(parent.ID)
	if err != nil {
		t.Fatalf("GroupGetByID(parent) error = %v", err)
	}
	if len(parentAfterRollback.Items) != 1 || !parentAfterRollback.Items[0].Disabled {
		t.Fatalf("failed graph update was not rolled back: %+v", parentAfterRollback.Items)
	}

	// leaf <- depth-1 <- depth-2 <- depth-3 合法；再包一层会形成 4 条嵌套边，必须拒绝。
	previousID := leaf.ID
	for depth := 1; depth <= MaxGroupNestDepth; depth++ {
		group := model.Group{
			Name: "depth-" + string(rune('0'+depth)),
			Mode: model.GroupModeManual,
			Items: []model.GroupItem{{
				Type:          model.GroupItemTypeGroup,
				TargetGroupID: &previousID,
				Priority:      1,
			}},
		}
		if err := GroupCreate(&group, ctx); err != nil {
			t.Fatalf("GroupCreate(depth %d) error = %v", depth, err)
		}
		previousID = group.ID
	}
	tooDeep := model.Group{
		Name: "too-deep",
		Mode: model.GroupModeManual,
		Items: []model.GroupItem{{
			Type:          model.GroupItemTypeGroup,
			TargetGroupID: &previousID,
			Priority:      1,
		}},
	}
	if err := GroupCreate(&tooDeep, ctx); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("too-deep error = %v, want depth error", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
