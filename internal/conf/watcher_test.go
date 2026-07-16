package conf

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWatcherReloadsValidConfigAndRejectsInvalidConfig(t *testing.T) {
	oldConfig := Current()
	t.Cleanup(func() { _ = Set(oldConfig) })
	path := filepath.Join(t.TempDir(), "config.json")
	writeWatcherConfig(t, path, 8080, "info")
	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	watcher, err := Watch(path)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	updates := watcher.Subscribe()

	writeWatcherConfig(t, path, 9090, "debug")
	updated := waitForConfig(t, updates)
	if updated.Server.Port != 9090 || updated.Log.Level != "debug" {
		t.Fatalf("reloaded config = %#v", updated)
	}
	if current := Current(); current.Server.Port != 9090 {
		t.Fatalf("Current().Server.Port = %d, want 9090", current.Server.Port)
	}

	if err := os.WriteFile(path, []byte(`{"server":`), 0600); err != nil {
		t.Fatalf("WriteFile(invalid) error = %v", err)
	}
	select {
	case config := <-updates:
		t.Fatalf("invalid config produced update: %#v", config)
	case <-time.After(300 * time.Millisecond):
	}
	if current := Current(); current.Server.Port != 9090 {
		t.Fatalf("invalid reload changed current port to %d", current.Server.Port)
	}

	replacement := filepath.Join(filepath.Dir(path), "config.next")
	writeWatcherConfig(t, replacement, 10080, "warn")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	updated = waitForConfig(t, updates)
	if updated.Server.Port != 10080 || updated.Log.Level != "warn" {
		t.Fatalf("atomically replaced config = %#v", updated)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "port", mutate: func(c *Config) { c.Server.Port = 70000 }},
		{name: "trusted proxy", mutate: func(c *Config) { c.Server.TrustedProxies = []string{"not-a-cidr"} }},
		{name: "session cookie secure mode", mutate: func(c *Config) { c.Server.SessionCookieSecure = "never" }},
		{name: "database", mutate: func(c *Config) { c.Database.Type = "oracle" }},
		{name: "log format", mutate: func(c *Config) { c.Log.Format = "xml" }},
		{name: "sample ratio", mutate: func(c *Config) { c.Observability.Tracing.SampleRatio = 2 }},
		{name: "public unauthenticated metrics", mutate: func(c *Config) {
			c.Observability.Metrics.Enabled = true
			c.Observability.Metrics.Host = "0.0.0.0"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Default()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("Validate() expected an error")
			}
		})
	}
}

func writeWatcherConfig(t *testing.T, path string, port int, level string) {
	t.Helper()
	contents := []byte(`{
		"server":{"host":"127.0.0.1","port":` + strconv.Itoa(port) + `},
		"database":{"type":"sqlite","path":"data.db"},
		"log":{"level":"` + level + `","format":"json"},
		"jwt":{"default_expiry_minutes":15,"max_expiry_days":30},
		"observability":{"metrics":{"enabled":true},"tracing":{"enabled":false,"endpoint":"localhost:4318","insecure":true,"sample_ratio":0.01}}
	}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func waitForConfig(t *testing.T, updates <-chan Config) Config {
	t.Helper()
	select {
	case config := <-updates:
		return config
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config reload")
		return Config{}
	}
}
