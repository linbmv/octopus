package requestrewrite

import (
	"reflect"
	"strings"
	"testing"
)

func TestProtectedAuthenticationHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "x-api-key", "X-Goog-Api-Key", "X-Auth-Token", "X-Session-Token", "X-Amz-Credential", "Cookie"} {
		if !IsProtectedHeader(name) {
			t.Fatalf("IsProtectedHeader(%q) = false", name)
		}
	}
	if IsProtectedHeader("X-Request-Trace") {
		t.Fatal("ordinary metadata header reported as protected")
	}
}

func TestParseAndApplyJSONPointer(t *testing.T) {
	tokens, err := ParseJSONPointer("/messages/0/content~1type")
	if err != nil {
		t.Fatalf("ParseJSONPointer() error = %v", err)
	}
	if !reflect.DeepEqual(tokens, []string{"messages", "0", "content/type"}) {
		t.Fatalf("tokens = %#v", tokens)
	}

	doc := any(map[string]any{
		"messages": []any{map[string]any{"content/type": "old", "remove": true}},
	})
	doc, changed, err := ApplyJSONPointer(doc, tokens, "override", "new")
	if err != nil || !changed {
		t.Fatalf("override changed=%v error=%v", changed, err)
	}
	remove, _ := ParseJSONPointer("/messages/0/remove")
	doc, changed, err = ApplyJSONPointer(doc, remove, "remove", nil)
	if err != nil || !changed {
		t.Fatalf("remove changed=%v error=%v", changed, err)
	}
	message := doc.(map[string]any)["messages"].([]any)[0].(map[string]any)
	if message["content/type"] != "new" {
		t.Fatalf("override result = %#v", message)
	}
	if _, exists := message["remove"]; exists {
		t.Fatalf("remove result = %#v", message)
	}
}

func TestJSONPointerBoundariesAndSafeNoop(t *testing.T) {
	for _, path := range []string{"", "model", "/", "/items/-", "/bad~2escape", "/" + strings.Repeat("x", MaxJSONPointerToken+1)} {
		if _, err := ParseJSONPointer(path); err == nil {
			t.Fatalf("ParseJSONPointer(%q) error = nil", path)
		}
	}

	tokens, _ := ParseJSONPointer("/missing/value")
	doc := any(map[string]any{"present": true})
	got, changed, err := ApplyJSONPointer(doc, tokens, "remove", nil)
	if err != nil || changed || !reflect.DeepEqual(got, doc) {
		t.Fatalf("missing path got=%#v changed=%v error=%v", got, changed, err)
	}
}
