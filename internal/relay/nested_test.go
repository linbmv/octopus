package relay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
)

func TestPickGroupItemSkipsDisabledMembers(t *testing.T) {
	group := model.Group{
		ID:      1,
		Enabled: true,
		Mode:    model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 10, Disabled: true, Priority: 1},
			{ID: 11, Priority: 2},
		},
	}
	if item := pickGroupItemSkipping(group, nil); item.ID != 11 {
		t.Fatalf("pickGroupItemSkipping() = %d, want 11", item.ID)
	}

	group.Mode = model.GroupModeManual
	group.ActiveItemID = 10
	if item := pickGroupItemSkipping(group, nil); item.ID != 0 {
		t.Fatalf("manual disabled item = %d, want no item", item.ID)
	}
}

func TestPickGroupItemSupportsRoundRobinAndWeightedModes(t *testing.T) {
	routeMu.Lock()
	delete(routes, 2001)
	delete(routes, 2002)
	routeMu.Unlock()
	t.Cleanup(func() {
		routeMu.Lock()
		delete(routes, 2001)
		delete(routes, 2002)
		routeMu.Unlock()
	})
	roundRobin := model.Group{ID: 2001, Enabled: true, Mode: model.GroupModeRoundRobin, Items: []model.GroupItem{
		{ID: 1, Priority: 1, Weight: 1}, {ID: 2, Priority: 2, Weight: 1},
	}}
	if first := pickGroupItemSkipping(roundRobin, nil); first.ID != 1 {
		t.Fatalf("round robin first = %d, want 1", first.ID)
	}
	if second := pickGroupItemSkipping(roundRobin, nil); second.ID != 2 {
		t.Fatalf("round robin second = %d, want 2", second.ID)
	}
	weighted := model.Group{ID: 2002, Enabled: true, Mode: model.GroupModeWeighted, Items: []model.GroupItem{
		{ID: 3, Priority: 1, Weight: 100}, {ID: 4, Priority: 2, Weight: 1, Disabled: true},
	}}
	if selected := pickGroupItemSkipping(weighted, nil); selected.ID != 3 {
		t.Fatalf("weighted disabled selection = %d, want 3", selected.ID)
	}
}

func TestPickGroupItemSkippingFallsBackWhenCurrentIsSkipped(t *testing.T) {
	routeMu.Lock()
	delete(routes, 3001)
	routeMu.Unlock()
	t.Cleanup(func() {
		routeMu.Lock()
		delete(routes, 3001)
		routeMu.Unlock()
	})

	group := model.Group{
		ID:      3001,
		Enabled: true,
		Mode:    model.GroupModeFailover,
		Items: []model.GroupItem{
			{ID: 3011, Priority: 1},
			{ID: 3012, Priority: 2},
		},
	}
	current := pickGroupItemSkipping(group, nil)
	if current.ID != 3011 {
		t.Fatalf("initial selection = %d, want 3011", current.ID)
	}

	fallback := pickGroupItemSkipping(group, map[int]struct{}{current.ID: {}})
	if fallback.ID != 3012 {
		t.Fatalf("skipped current selection = %d, want 3012", fallback.ID)
	}
}

func TestPickGroupLeafGuardsContextCycleAndDepth(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pickGroupLeaf(canceled, model.Group{ID: 1, Enabled: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}

	group := model.Group{ID: 7, Enabled: true}
	if _, err := pickGroupLeafAt(context.Background(), group, 0, map[int]struct{}{7: {}}); err == nil {
		t.Fatal("dirty cyclic graph was not rejected")
	}
	if _, err := pickGroupLeafAt(context.Background(), group, op.MaxGroupNestDepth+1, map[int]struct{}{}); err == nil {
		t.Fatal("over-depth graph was not rejected")
	}
}

func TestPickGroupLeafReturnsDirectEnabledMember(t *testing.T) {
	channelModelID := 42
	channelModel := &model.ChannelModel{ID: channelModelID, Name: "upstream-model"}
	group := model.Group{
		ID:      1,
		Enabled: true,
		Mode:    model.GroupModeManual,
		Items: []model.GroupItem{{
			ID:             9,
			Type:           model.GroupItemTypeChannelModel,
			ChannelModelID: &channelModelID,
			ChannelModel:   channelModel,
		}},
		ActiveItemID: 9,
	}
	leaf, err := pickGroupLeaf(context.Background(), group)
	if err != nil {
		t.Fatalf("pickGroupLeaf() error = %v", err)
	}
	if leaf == nil || leaf.group.ID != group.ID || leaf.item.ID != 9 || len(leaf.path) != 1 {
		t.Fatalf("unexpected leaf: %+v", leaf)
	}
}

func TestRecordRouteFailurePathBubblesManualNestedFailure(t *testing.T) {
	routeMu.Lock()
	delete(routes, 1001)
	routeMu.Unlock()
	t.Cleanup(func() {
		routeMu.Lock()
		delete(routes, 1001)
		routeMu.Unlock()
	})
	parent := model.Group{
		ID:      1001,
		Enabled: true,
		Mode:    model.GroupModeFailover,
		RelayConfig: model.GroupRelayConfig{
			MemberMaxAttempts:     1,
			MemberCooldownSeconds: 1,
		},
		Items: []model.GroupItem{{ID: 1002, Priority: 1}},
	}
	child := model.Group{ID: 1003, Enabled: true, Mode: model.GroupModeManual, Items: []model.GroupItem{{ID: 1004}}}
	if selected := pickGroupItemSkipping(parent, nil); selected.ID != 1002 {
		t.Fatalf("parent selection = %d, want 1002", selected.ID)
	}
	if cooled := recordRouteFailurePath([]groupPathItem{
		{group: parent, item: parent.Items[0]},
		{group: child, item: child.Items[0]},
	}, 1); !cooled {
		t.Fatal("manual nested failure did not switch the enclosing failover group")
	}
	routeMu.Lock()
	route := routes[parent.ID]
	routeMu.Unlock()
	if route.CurrentItemID != 0 || route.Cooldowns[parent.Items[0].ID] == 0 {
		t.Fatalf("parent route was not cooled: %+v", route)
	}
}

func TestPickGroupLeafResolvesNestedGroupAndHonorsToggles(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "relay-test.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	channel := model.Channel{
		Name:    "nested-channel",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		Models:  []model.ChannelModel{{Name: "nested-model", Source: model.ChannelModelSourceManual}},
	}
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("create channel error = %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache() error = %v", err)
	}

	channelModelID := channel.Models[0].ID
	leafGroup := model.Group{
		Name: "nested-leaf",
		Mode: model.GroupModeManual,
		Items: []model.GroupItem{{
			Type:           model.GroupItemTypeChannelModel,
			ChannelModelID: &channelModelID,
			Priority:       1,
		}},
	}
	if err := op.GroupCreate(&leafGroup, ctx); err != nil {
		t.Fatalf("create leaf group error = %v", err)
	}
	leafItemID := leafGroup.Items[0].ID
	if _, err := op.GroupActiveItemUpdate(leafGroup.ID, &model.GroupActiveItemUpdateRequest{ItemID: &leafItemID}, ctx); err != nil {
		t.Fatalf("activate leaf error = %v", err)
	}

	rootGroup := model.Group{
		Name: "nested-root",
		Mode: model.GroupModeManual,
		Items: []model.GroupItem{{
			Type:          model.GroupItemTypeGroup,
			TargetGroupID: &leafGroup.ID,
			Priority:      1,
		}},
	}
	if err := op.GroupCreate(&rootGroup, ctx); err != nil {
		t.Fatalf("create root group error = %v", err)
	}
	rootItemID := rootGroup.Items[0].ID
	if _, err := op.GroupActiveItemUpdate(rootGroup.ID, &model.GroupActiveItemUpdateRequest{ItemID: &rootItemID}, ctx); err != nil {
		t.Fatalf("activate root error = %v", err)
	}
	rootGroup, _ = op.GroupGetByID(rootGroup.ID)

	leaf, err := pickGroupLeaf(ctx, rootGroup)
	if err != nil {
		t.Fatalf("pick nested leaf error = %v", err)
	}
	if leaf == nil || leaf.item.ID != leafItemID || len(leaf.path) != 2 {
		t.Fatalf("unexpected nested leaf: %+v", leaf)
	}

	// A disabled child is skipped without cooling the parent member, so a
	// failover parent can use its next member and immediately return to the
	// nested member when the child is re-enabled.
	failoverMode := model.GroupModeFailover
	updatedRoot, err := op.GroupUpdate(&model.GroupUpdateRequest{
		ID:   rootGroup.ID,
		Mode: &failoverMode,
		ItemsToAdd: []model.GroupItemAddRequest{{
			Type:           model.GroupItemTypeChannelModel,
			ChannelModelID: channelModelID,
			Priority:       2,
		}},
	}, ctx)
	if err != nil {
		t.Fatalf("add failover fallback member error = %v", err)
	}
	var directItemID int
	for _, item := range updatedRoot.Items {
		if item.Type == model.GroupItemTypeChannelModel {
			directItemID = item.ID
		}
	}
	if directItemID == 0 {
		t.Fatal("failover fallback member was not created")
	}
	routeMu.Lock()
	delete(routes, rootGroup.ID)
	routeMu.Unlock()

	disabled := false
	if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: leafGroup.ID, Enabled: &disabled}, ctx); err != nil {
		t.Fatalf("disable nested target error = %v", err)
	}
	rootGroup, _ = op.GroupGetByID(rootGroup.ID)
	leaf, err = pickGroupLeaf(ctx, rootGroup)
	if err != nil || leaf == nil || leaf.item.ID != directItemID {
		t.Fatalf("disabled nested group did not fall back to direct member: leaf=%+v err=%v", leaf, err)
	}

	enabled := true
	if _, err := op.GroupUpdate(&model.GroupUpdateRequest{ID: leafGroup.ID, Enabled: &enabled}, ctx); err != nil {
		t.Fatalf("re-enable nested target error = %v", err)
	}
	rootGroup, _ = op.GroupGetByID(rootGroup.ID)
	leaf, err = pickGroupLeaf(ctx, rootGroup)
	if err != nil || leaf == nil || leaf.item.ID != leafItemID {
		t.Fatalf("re-enabled nested group was not selected immediately: leaf=%+v err=%v", leaf, err)
	}
}
