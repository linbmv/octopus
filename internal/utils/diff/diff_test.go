package diff

import (
	"reflect"
	"sort"
	"testing"
)

func TestDiffPreservesDuplicateCounts(t *testing.T) {
	deleted, added := Diff([]string{"a", "a", "b"}, []string{"a", "c", "c"})
	sort.Strings(deleted)
	sort.Strings(added)
	if !reflect.DeepEqual(deleted, []string{"a", "b"}) {
		t.Fatalf("deleted = %v", deleted)
	}
	if !reflect.DeepEqual(added, []string{"c", "c"}) {
		t.Fatalf("added = %v", added)
	}
}

func TestDiffEqualAndEmptyInputs(t *testing.T) {
	deleted, added := Diff([]int{1, 2}, []int{1, 2})
	if len(deleted) != 0 || len(added) != 0 {
		t.Fatalf("equal slices produced deleted=%v added=%v", deleted, added)
	}
	deleted, added = Diff([]int(nil), []int{3})
	if len(deleted) != 0 || !reflect.DeepEqual(added, []int{3}) {
		t.Fatalf("empty old produced deleted=%v added=%v", deleted, added)
	}
}
