package op

import (
	"reflect"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm/clause"
)

func TestChannelKeyUpdatesResetsRuntimeStateWhenKeyBecomesUsable(t *testing.T) {
	enabled := true
	channelKey := "replacement-key"
	remark := "rotated"

	updates := channelKeyUpdates(model.ChannelKeyUpdateRequest{
		Enabled:    &enabled,
		ChannelKey: &channelKey,
		Remark:     &remark,
	})

	want := map[string]interface{}{
		"enabled":             true,
		"channel_key":         "replacement-key",
		"remark":              "rotated",
		"status_code":         0,
		"last_use_time_stamp": 0,
	}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("channelKeyUpdates() = %#v, want %#v", updates, want)
	}
}

func TestChannelKeyUpdatesKeepsRuntimeStateWhenOnlyDisabling(t *testing.T) {
	enabled := false

	updates := channelKeyUpdates(model.ChannelKeyUpdateRequest{Enabled: &enabled})

	want := map[string]interface{}{"enabled": false}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("channelKeyUpdates() = %#v, want %#v", updates, want)
	}
}

func TestBuildGroupItemUpdatesBuildsScopedCaseExpressions(t *testing.T) {
	disabled := true
	items := []model.GroupItemUpdateRequest{
		{ID: 11, Priority: intPointer(2), Weight: intPointer(3), Disabled: &disabled},
		{ID: 12, Priority: intPointer(4), Weight: intPointer(5)},
	}

	ids, updates := buildGroupItemUpdates(items)

	if !reflect.DeepEqual(ids, []int{11, 12}) {
		t.Fatalf("ids = %v, want [11 12]", ids)
	}
	assertClauseSQL(t, updates["priority"], "CASE id WHEN 11 THEN 2 WHEN 12 THEN 4 ELSE priority END")
	assertClauseSQL(t, updates["weight"], "CASE id WHEN 11 THEN 3 WHEN 12 THEN 5 ELSE weight END")
	assertClauseSQL(t, updates["disabled"], "CASE id WHEN 11 THEN true ELSE disabled END")
}

func TestBuildGroupItemUpdatesOmitsDisabledWithoutExplicitChange(t *testing.T) {
	_, updates := buildGroupItemUpdates([]model.GroupItemUpdateRequest{{ID: 7, Priority: intPointer(1), Weight: intPointer(9)}})

	if _, ok := updates["disabled"]; ok {
		t.Fatal("disabled expression should be omitted without an explicit change")
	}
}

func TestBuildGroupItemUpdatesPreservesOmittedFields(t *testing.T) {
	disabled := false
	_, updates := buildGroupItemUpdates([]model.GroupItemUpdateRequest{{ID: 7, Disabled: &disabled}})

	if _, ok := updates["priority"]; ok {
		t.Fatal("priority expression should be omitted without an explicit change")
	}
	if _, ok := updates["weight"]; ok {
		t.Fatal("weight expression should be omitted without an explicit change")
	}
}

func intPointer(value int) *int {
	return &value
}

func assertClauseSQL(t *testing.T, value interface{}, want string) {
	t.Helper()
	expr, ok := value.(clause.Expr)
	if !ok {
		t.Fatalf("value type = %T, want clause.Expr", value)
	}
	if expr.SQL != want {
		t.Fatalf("expression SQL = %q, want %q", expr.SQL, want)
	}
}
