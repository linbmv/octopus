package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestLocaleFilesHaveIdenticalLeafKeys(t *testing.T) {
	files := []string{"en.json", "zh_hans.json", "zh_hant.json"}
	keySets := make(map[string][]string, len(files))
	for _, file := range files {
		path := filepath.Join("..", "..", "web", "public", "locale", file)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var value map[string]any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		keys := make([]string, 0, 512)
		collectLocaleLeafKeys(value, "", &keys)
		sort.Strings(keys)
		keySets[file] = keys
	}

	want := keySets[files[0]]
	for _, file := range files[1:] {
		if !reflect.DeepEqual(keySets[file], want) {
			t.Fatalf("locale key mismatch: %s has %d leaves, %s has %d", files[0], len(want), file, len(keySets[file]))
		}
	}
}

func collectLocaleLeafKeys(value map[string]any, prefix string, keys *[]string) {
	for key, child := range value {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := child.(map[string]any); ok {
			collectLocaleLeafKeys(nested, path, keys)
			continue
		}
		*keys = append(*keys, path)
	}
}
