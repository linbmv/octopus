package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedContractsAreCurrent(t *testing.T) {
	generated, err := Generate()
	if err != nil {
		t.Fatalf("Generate(): %v", err)
	}

	path := filepath.Join("..", "..", "web", "src", "api", "contracts.ts")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(current, generated) {
		t.Fatalf("%s is out of date; run go run ./tools/webtypes > web/src/api/contracts.ts", path)
	}
}
