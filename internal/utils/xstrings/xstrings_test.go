package xstrings

import (
	"reflect"
	"testing"
)

func TestSplitTrimCompact(t *testing.T) {
	tests := []struct {
		name  string
		sep   string
		parts []string
		want  []string
	}{
		{name: "no parts", sep: ",", want: []string{}},
		{name: "multiple parts", sep: ",", parts: []string{"a, b", "c"}, want: []string{"a", "b", "c"}},
		{name: "trim unicode whitespace", sep: ",", parts: []string{"\u2003alpha\u2003,\tbeta\n"}, want: []string{"alpha", "beta"}},
		{name: "drop empty fields", sep: ",", parts: []string{"a,,, ,b,"}, want: []string{"a", "b"}},
		{name: "non comma separator", sep: "|", parts: []string{"left | middle|| right"}, want: []string{"left", "middle", "right"}},
		{name: "preserve order and duplicates", sep: ",", parts: []string{"b,a,b"}, want: []string{"b", "a", "b"}},
		{name: "all empty", sep: ",", parts: []string{"", " , \t,\n"}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitTrimCompact(tt.sep, tt.parts...)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SplitTrimCompact(%q, %q) = %#v, want %#v", tt.sep, tt.parts, got, tt.want)
			}
		})
	}
}

func TestTrimCompact(t *testing.T) {
	tests := []struct {
		name  string
		items []string
		want  []string
	}{
		{name: "nil input", items: nil, want: []string{}},
		{name: "trim and compact", items: []string{" a ", "", "\tb\n", "   ", "c"}, want: []string{"a", "b", "c"}},
		{name: "unicode whitespace", items: []string{"\u2003你好\u2003", "\u00a0"}, want: []string{"你好"}},
		{name: "preserve order and duplicates", items: []string{"b", "a", "b"}, want: []string{"b", "a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimCompact(tt.items)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("TrimCompact(%q) = %#v, want %#v", tt.items, got, tt.want)
			}
		})
	}
}
