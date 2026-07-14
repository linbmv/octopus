package op

import (
	"testing"
)

func TestPatchHelper_ApplyField_WithPointer(t *testing.T) {
	helper := NewPatchHelper()

	name := "test-channel"
	enabled := true

	helper.ApplyField("name", &name)
	helper.ApplyField("enabled", &enabled)

	if len(helper.SelectFields()) != 2 {
		t.Errorf("expected 2 fields, got %d", len(helper.SelectFields()))
	}

	if helper.Updates()["name"] != "test-channel" {
		t.Errorf("expected name='test-channel', got %v", helper.Updates()["name"])
	}

	if helper.Updates()["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", helper.Updates()["enabled"])
	}
}

func TestPatchHelper_ApplyField_WithNil(t *testing.T) {
	helper := NewPatchHelper()

	var name *string = nil
	helper.ApplyField("name", name)

	if len(helper.SelectFields()) != 0 {
		t.Errorf("expected 0 fields for nil pointer, got %d", len(helper.SelectFields()))
	}
}

func TestPatchHelper_ApplyField_WithNonPointer(t *testing.T) {
	helper := NewPatchHelper()

	helper.ApplyField("count", 42)
	helper.ApplyField("ratio", 0.5)

	if len(helper.SelectFields()) != 2 {
		t.Errorf("expected 2 fields, got %d", len(helper.SelectFields()))
	}

	if helper.Updates()["count"] != 42 {
		t.Errorf("expected count=42, got %v", helper.Updates()["count"])
	}

	if helper.Updates()["ratio"] != 0.5 {
		t.Errorf("expected ratio=0.5, got %v", helper.Updates()["ratio"])
	}
}

func TestPatchHelper_HasUpdates(t *testing.T) {
	helper := NewPatchHelper()

	if helper.HasUpdates() {
		t.Error("expected HasUpdates()=false for empty helper")
	}

	name := "test"
	helper.ApplyField("name", &name)

	if !helper.HasUpdates() {
		t.Error("expected HasUpdates()=true after adding field")
	}
}

func TestPatchHelper_MixedTypes(t *testing.T) {
	helper := NewPatchHelper()

	name := "channel-1"
	enabled := true
	var proxy *string = nil
	rpmLimit := 100

	helper.ApplyField("name", &name)
	helper.ApplyField("enabled", &enabled)
	helper.ApplyField("proxy", proxy) // nil, should be ignored
	helper.ApplyField("rpm_limit", &rpmLimit)

	if len(helper.SelectFields()) != 3 {
		t.Errorf("expected 3 fields (ignoring nil), got %d", len(helper.SelectFields()))
	}

	expected := map[string]interface{}{
		"name":      "channel-1",
		"enabled":   true,
		"rpm_limit": 100,
	}

	for k, v := range expected {
		if helper.Updates()[k] != v {
			t.Errorf("expected %s=%v, got %v", k, v, helper.Updates()[k])
		}
	}
}
