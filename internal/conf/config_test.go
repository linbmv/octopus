package conf

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestLoadUsesDefaultValues(t *testing.T) {
	resetConfigState(t)
	path := writeConfigFile(t, `{}`)

	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	config := Current()
	if config.Server.Host != "0.0.0.0" || config.Server.Port != 8080 {
		t.Fatalf("Server = %#v, want default host and port", config.Server)
	}
	if len(config.Server.TrustedProxies) != 0 {
		t.Fatalf("Server.TrustedProxies = %#v, want secure empty default", config.Server.TrustedProxies)
	}
	if config.Server.SessionCookieSecure != "auto" {
		t.Fatalf("Server.SessionCookieSecure = %q, want auto", config.Server.SessionCookieSecure)
	}
	if config.Database.Type != "sqlite" || config.Database.Path != "data/data.db" {
		t.Fatalf("Database = %#v, want SQLite defaults", config.Database)
	}
	if config.Log.Level != "info" {
		t.Fatalf("Log.Level = %q, want info", config.Log.Level)
	}
	if config.Log.Format != "json" {
		t.Fatalf("Log.Format = %q, want json", config.Log.Format)
	}
	if config.JWT.DefaultExpiryMinutes != 15 || config.JWT.MaxExpiryDays != 30 {
		t.Fatalf("JWT = %#v, want default expiry values", config.JWT)
	}
	if config.Relay.NonStreamTimeoutSeconds != 600 {
		t.Fatalf("Relay.NonStreamTimeoutSeconds = %d, want 600", config.Relay.NonStreamTimeoutSeconds)
	}
	if config.Relay.StreamFirstEventTimeoutSeconds != 600 || config.Relay.StreamIdleTimeoutSeconds != 600 {
		t.Fatalf("Relay stream timeout defaults = %#v, want 600 seconds each", config.Relay)
	}
	if config.Relay.NonStreamAttemptTimeoutSeconds != 60 {
		t.Fatalf("Relay.NonStreamAttemptTimeoutSeconds = %d, want 60", config.Relay.NonStreamAttemptTimeoutSeconds)
	}
	if config.Relay.StreamColdStartFirstEventTimeoutSeconds != 30 {
		t.Fatalf("Relay.StreamColdStartFirstEventTimeoutSeconds = %d, want 30", config.Relay.StreamColdStartFirstEventTimeoutSeconds)
	}
	if config.Relay.MaxUpstreamAttempts != 8 || config.Relay.StreamFirstEventBudgetSeconds != 120 {
		t.Fatalf("Relay request budget defaults = %#v, want attempts=8 budget=120s", config.Relay)
	}
	if config.Relay.MaxJSONRequestBytes != 32<<20 || config.Relay.MaxImageRequestBytes != 64<<20 || config.Relay.MaxNonStreamResponseBytes != 64<<20 {
		t.Fatalf("Relay body limit defaults = %#v", config.Relay)
	}
	if config.Observability.Metrics.Enabled || config.Observability.Tracing.Enabled {
		t.Fatalf("Observability defaults = %#v", config.Observability)
	}
	if config.WebAuthn.Enabled || config.WebAuthn.RPID != "" || config.WebAuthn.RPDisplayName != APP_NAME || len(config.WebAuthn.RPOrigins) != 0 {
		t.Fatalf("WebAuthn defaults = %#v", config.WebAuthn)
	}
	if config.SelfHealing.Enabled || config.SelfHealing.CaptureSuccessBaselines ||
		config.SelfHealing.BaselineTTLSeconds != 86400 ||
		config.SelfHealing.SentinelIntervalSeconds != 1800 ||
		config.SelfHealing.FailureThreshold != 3 ||
		config.SelfHealing.FailureWindowSeconds != 300 {
		t.Fatalf("SelfHealing defaults = %#v", config.SelfHealing)
	}
	diagnostic := config.SelfHealing.Diagnostic
	if diagnostic.MaxVariants != 8 || diagnostic.MaxConcurrency != 1 || diagnostic.QueueDepth != 16 ||
		diagnostic.RequestsPerMinute != 6 || diagnostic.TimeoutSeconds != 30 || diagnostic.SessionTTLSeconds != 300 ||
		diagnostic.CostPerRequestUSD != 0.001 || diagnostic.MaxBatchCostUSD != 0.01 || diagnostic.MaxTotalCostUSD != 0.05 {
		t.Fatalf("SelfHealing.Diagnostic defaults = %#v", diagnostic)
	}
	if config.Observability.Metrics.Host != "127.0.0.1" || config.Observability.Metrics.Port != 9090 || config.Observability.Metrics.BearerToken != "" || len(config.Observability.Metrics.Allowlist) != 0 {
		t.Fatalf("Metrics defaults = %#v", config.Observability.Metrics)
	}
}

func TestValidateSelfHealingBaselineTTL(t *testing.T) {
	for _, ttl := range []int{59, 90*24*60*60 + 1} {
		config := Default()
		config.SelfHealing.BaselineTTLSeconds = ttl
		if err := Validate(config); err == nil || !strings.Contains(err.Error(), "self_healing.baseline_ttl_seconds") {
			t.Fatalf("Validate() ttl=%d error = %v, want self-healing TTL validation", ttl, err)
		}
	}
}

func TestValidateSelfHealingLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		set  func(*Config)
	}{
		{name: "sentinel interval low", want: "sentinel_interval_seconds", set: func(c *Config) { c.SelfHealing.SentinelIntervalSeconds = 59 }},
		{name: "sentinel interval high", want: "sentinel_interval_seconds", set: func(c *Config) { c.SelfHealing.SentinelIntervalSeconds = 86401 }},
		{name: "failure threshold low", want: "failure_threshold", set: func(c *Config) { c.SelfHealing.FailureThreshold = 0 }},
		{name: "failure threshold high", want: "failure_threshold", set: func(c *Config) { c.SelfHealing.FailureThreshold = 101 }},
		{name: "failure window low", want: "failure_window_seconds", set: func(c *Config) { c.SelfHealing.FailureWindowSeconds = 59 }},
		{name: "failure window high", want: "failure_window_seconds", set: func(c *Config) { c.SelfHealing.FailureWindowSeconds = 86401 }},
		{name: "variants low", want: "diagnostic.max_variants", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxVariants = 0 }},
		{name: "variants high", want: "diagnostic.max_variants", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxVariants = 17 }},
		{name: "concurrency low", want: "diagnostic.max_concurrency", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxConcurrency = 0 }},
		{name: "concurrency high", want: "diagnostic.max_concurrency", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxConcurrency = 9 }},
		{name: "queue low", want: "diagnostic.queue_depth", set: func(c *Config) { c.SelfHealing.Diagnostic.QueueDepth = 0 }},
		{name: "queue high", want: "diagnostic.queue_depth", set: func(c *Config) { c.SelfHealing.Diagnostic.QueueDepth = 257 }},
		{name: "rpm low", want: "diagnostic.requests_per_minute", set: func(c *Config) { c.SelfHealing.Diagnostic.RequestsPerMinute = 0 }},
		{name: "rpm high", want: "diagnostic.requests_per_minute", set: func(c *Config) { c.SelfHealing.Diagnostic.RequestsPerMinute = 601 }},
		{name: "timeout low", want: "diagnostic.timeout_seconds", set: func(c *Config) { c.SelfHealing.Diagnostic.TimeoutSeconds = 0 }},
		{name: "timeout high", want: "diagnostic.timeout_seconds", set: func(c *Config) { c.SelfHealing.Diagnostic.TimeoutSeconds = 301 }},
		{name: "session ttl low", want: "diagnostic.session_ttl_seconds", set: func(c *Config) { c.SelfHealing.Diagnostic.SessionTTLSeconds = 59 }},
		{name: "session ttl high", want: "diagnostic.session_ttl_seconds", set: func(c *Config) { c.SelfHealing.Diagnostic.SessionTTLSeconds = 3601 }},
		{name: "request cost zero", want: "diagnostic.cost_per_request_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.CostPerRequestUSD = 0 }},
		{name: "request cost high", want: "diagnostic.cost_per_request_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.CostPerRequestUSD = 10.01 }},
		{name: "batch cost zero", want: "diagnostic.max_batch_cost_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxBatchCostUSD = 0 }},
		{name: "batch cost high", want: "diagnostic.max_batch_cost_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxBatchCostUSD = 1000.01 }},
		{name: "total cost zero", want: "diagnostic.max_total_cost_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxTotalCostUSD = 0 }},
		{name: "total cost high", want: "diagnostic.max_total_cost_usd", set: func(c *Config) { c.SelfHealing.Diagnostic.MaxTotalCostUSD = 10000.01 }},
		{name: "batch exceeds total", want: "must not exceed", set: func(c *Config) {
			c.SelfHealing.Diagnostic.MaxBatchCostUSD = 1
			c.SelfHealing.Diagnostic.MaxTotalCostUSD = 0.5
		}},
		{name: "too many extra user agents", want: "extra_user_agents", set: func(c *Config) {
			c.SelfHealing.Diagnostic.ExtraUserAgents = make([]string, 9)
		}},
		{name: "malformed extra header", want: "extra_headers", set: func(c *Config) {
			c.SelfHealing.Diagnostic.ExtraHeaders = []string{"no-colon-entry"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Default()
			test.set(&config)
			if err := Validate(config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSessionCookieSecureMode(t *testing.T) {
	config := Default()
	for _, mode := range []string{"auto", "always"} {
		config.Server.SessionCookieSecure = mode
		if err := Validate(config); err != nil {
			t.Fatalf("Validate() rejected mode %q: %v", mode, err)
		}
	}
	config.Server.SessionCookieSecure = "never"
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "server.session_cookie_secure") {
		t.Fatalf("Validate() error = %v, want secure-cookie mode error", err)
	}
}

func TestValidateRejectsUnsafeNonStreamTimeout(t *testing.T) {
	config := Default()
	for _, timeout := range []int{-1, 24*60*60 + 1} {
		config.Relay.NonStreamTimeoutSeconds = timeout
		if err := Validate(config); err == nil || !strings.Contains(err.Error(), "relay.non_stream_timeout_seconds") {
			t.Fatalf("Validate() timeout=%d error = %v, want relay timeout validation error", timeout, err)
		}
	}

	config.Relay.NonStreamTimeoutSeconds = 0
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() should allow an explicitly disabled timeout: %v", err)
	}
}

func TestValidateRejectsUnsafeRelayResourceLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "first event timeout negative", mutate: func(c *Config) { c.Relay.StreamFirstEventTimeoutSeconds = -1 }},
		{name: "first event timeout too large", mutate: func(c *Config) { c.Relay.StreamFirstEventTimeoutSeconds = 86401 }},
		{name: "idle timeout negative", mutate: func(c *Config) { c.Relay.StreamIdleTimeoutSeconds = -1 }},
		{name: "idle timeout too large", mutate: func(c *Config) { c.Relay.StreamIdleTimeoutSeconds = 86401 }},
		{name: "non-stream attempt timeout negative", mutate: func(c *Config) { c.Relay.NonStreamAttemptTimeoutSeconds = -1 }},
		{name: "non-stream attempt timeout too large", mutate: func(c *Config) { c.Relay.NonStreamAttemptTimeoutSeconds = 86401 }},
		{name: "cold start timeout negative", mutate: func(c *Config) { c.Relay.StreamColdStartFirstEventTimeoutSeconds = -1 }},
		{name: "cold start timeout too large", mutate: func(c *Config) { c.Relay.StreamColdStartFirstEventTimeoutSeconds = 86401 }},
		{name: "attempt budget negative", mutate: func(c *Config) { c.Relay.MaxUpstreamAttempts = -1 }},
		{name: "attempt budget too large", mutate: func(c *Config) { c.Relay.MaxUpstreamAttempts = 1001 }},
		{name: "stream budget negative", mutate: func(c *Config) { c.Relay.StreamFirstEventBudgetSeconds = -1 }},
		{name: "stream budget too large", mutate: func(c *Config) { c.Relay.StreamFirstEventBudgetSeconds = 86401 }},
		{name: "JSON body disabled", mutate: func(c *Config) { c.Relay.MaxJSONRequestBytes = 0 }},
		{name: "image body disabled", mutate: func(c *Config) { c.Relay.MaxImageRequestBytes = 0 }},
		{name: "response body disabled", mutate: func(c *Config) { c.Relay.MaxNonStreamResponseBytes = 0 }},
		{name: "response body too large", mutate: func(c *Config) { c.Relay.MaxNonStreamResponseBytes = 1<<30 + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Default()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("Validate() expected an error")
			}
		})
	}

	config := Default()
	config.Relay.StreamFirstEventTimeoutSeconds = 0
	config.Relay.StreamIdleTimeoutSeconds = 0
	if err := Validate(config); err != nil {
		t.Fatalf("zero stream timeouts should explicitly disable their guards: %v", err)
	}
}

func TestValidateMetricsExposureRequiresAuthentication(t *testing.T) {
	config := Default()
	config.Observability.Metrics.Enabled = true
	config.Observability.Metrics.Host = "0.0.0.0"
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "bearer_token is required") {
		t.Fatalf("Validate() error = %v, want non-loopback authentication error", err)
	}

	config.Observability.Metrics.BearerToken = "too-short"
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "at least 16 bytes") {
		t.Fatalf("Validate() error = %v, want short-token error", err)
	}

	config.Observability.Metrics.BearerToken = "metrics-test-metrics-test"
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected authenticated non-loopback metrics: %v", err)
	}

	config.Observability.Metrics.Host = "127.0.0.1"
	config.Observability.Metrics.BearerToken = ""
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected loopback-only metrics without a token: %v", err)
	}

	config.Observability.Metrics.Allowlist = []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected valid metrics allowlist: %v", err)
	}
	config.Observability.Metrics.Allowlist = []string{"not-an-address"}
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "allowlist entry") {
		t.Fatalf("Validate() error = %v, want invalid metrics allowlist error", err)
	}
}

func TestValidateWebAuthnConfiguration(t *testing.T) {
	config := Default()
	config.WebAuthn.Enabled = true
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "rp_id") {
		t.Fatalf("Validate() error = %v, want missing rp_id", err)
	}
	config.WebAuthn.RPID = "example.com"
	config.WebAuthn.RPOrigins = []string{"http://example.com"}
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("Validate() error = %v, want insecure origin rejection", err)
	}
	config.WebAuthn.RPOrigins = []string{"https://example.com/path"}
	if err := Validate(config); err == nil || !strings.Contains(err.Error(), "must be an origin") {
		t.Fatalf("Validate() error = %v, want origin path rejection", err)
	}
	config.WebAuthn.RPOrigins = []string{"https://example.com"}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected production WebAuthn config: %v", err)
	}
	config.WebAuthn.RPID = "localhost"
	config.WebAuthn.RPOrigins = []string{"http://localhost:8080"}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected loopback development WebAuthn config: %v", err)
	}
}

func TestValidateTrustedProxyCIDRs(t *testing.T) {
	config := Default()
	config.Server.TrustedProxies = []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() rejected valid trusted proxies: %v", err)
	}

	for _, invalid := range []string{"", "proxy.example.com", "10.0.0.0/99"} {
		config.Server.TrustedProxies = []string{invalid}
		if err := Validate(config); err == nil || !strings.Contains(err.Error(), "server.trusted_proxies") {
			t.Fatalf("Validate() proxy=%q error = %v, want trusted proxy validation error", invalid, err)
		}
	}
}

func TestSetAndCurrentDefensivelyCopyTrustedProxies(t *testing.T) {
	resetConfigState(t)
	config := Default()
	config.Server.TrustedProxies = []string{"127.0.0.1"}
	if err := Set(config); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	config.Server.TrustedProxies[0] = "10.0.0.1"
	loaded := Current()
	if loaded.Server.TrustedProxies[0] != "127.0.0.1" {
		t.Fatalf("stored trusted proxies were aliased: %#v", loaded.Server.TrustedProxies)
	}
	loaded.Server.TrustedProxies[0] = "192.0.2.1"
	if got := Current().Server.TrustedProxies[0]; got != "127.0.0.1" {
		t.Fatalf("Current() exposed mutable trusted proxies: %q", got)
	}
}

func TestLoadEnvironmentOverridesFileValues(t *testing.T) {
	resetConfigState(t)
	path := writeConfigFile(t, `{
		"server": {"host": "127.0.0.1", "port": 9000},
		"log": {"level": "warn"},
		"database": {"type": "sqlite", "path": "file.db"}
	}`)
	t.Setenv("OCTOPUS_SERVER_PORT", "9443")
	t.Setenv("OCTOPUS_LOG_LEVEL", "debug")
	t.Setenv("OCTOPUS_DATABASE_TYPE", "postgres")
	t.Setenv("OCTOPUS_RELAY_NON_STREAM_TIMEOUT_SECONDS", "42")
	t.Setenv("OCTOPUS_RELAY_STREAM_FIRST_EVENT_TIMEOUT_SECONDS", "43")
	t.Setenv("OCTOPUS_RELAY_STREAM_IDLE_TIMEOUT_SECONDS", "44")
	t.Setenv("OCTOPUS_RELAY_MAX_NON_STREAM_RESPONSE_BYTES", "1048576")

	if err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	config := Current()
	if config.Server.Host != "127.0.0.1" || config.Server.Port != 9443 {
		t.Fatalf("Server = %#v, want file host and environment port", config.Server)
	}
	if config.Log.Level != "debug" {
		t.Fatalf("Log.Level = %q, want debug", config.Log.Level)
	}
	if config.Database.Type != "postgres" || config.Database.Path != "file.db" {
		t.Fatalf("Database = %#v, want environment type and file path", config.Database)
	}
	if config.Relay.NonStreamTimeoutSeconds != 42 {
		t.Fatalf("Relay.NonStreamTimeoutSeconds = %d, want environment value 42", config.Relay.NonStreamTimeoutSeconds)
	}
	if config.Relay.StreamFirstEventTimeoutSeconds != 43 || config.Relay.StreamIdleTimeoutSeconds != 44 || config.Relay.MaxNonStreamResponseBytes != 1048576 {
		t.Fatalf("Relay environment overrides = %#v", config.Relay)
	}
}

func TestLoadCreatesDefaultConfigWhenMissing(t *testing.T) {
	resetConfigState(t)
	t.Chdir(t.TempDir())

	if err := Load(""); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join("data", "config.json")); err != nil {
		t.Fatalf("default config was not created: %v", err)
	}
	if Current().Server.Port != 8080 {
		t.Fatalf("Server.Port = %d, want 8080", Current().Server.Port)
	}
}

func TestLoadFailsWhenDefaultConfigCannotBeCreated(t *testing.T) {
	resetConfigState(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile("data", []byte("not a directory"), 0600); err != nil {
		t.Fatalf("create blocking data file: %v", err)
	}

	err := Load("")
	if err == nil || !strings.Contains(err.Error(), "create default config directory") {
		t.Fatalf("Load() error = %v, want default directory creation error", err)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	resetConfigState(t)
	path := writeConfigFile(t, `{"server":`)

	err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "error reading config file") {
		t.Fatalf("Load() error = %v, want config read error", err)
	}
}

func TestIsDebug(t *testing.T) {
	t.Setenv("OCTOPUS_DEBUG", "true")
	if !IsDebug() {
		t.Fatal("IsDebug() = false, want true")
	}
	t.Setenv("OCTOPUS_DEBUG", "TRUE")
	if IsDebug() {
		t.Fatal("IsDebug() = true for non-lowercase value")
	}
}

func TestFormatDate(t *testing.T) {
	tests := map[string]string{
		"":                     "unknown",
		"unknown":              "unknown",
		"2026-07-14":           "2026-07-14 00:00",
		"2026-07-14T15:30:00Z": "2026-07-14 15:30",
		"not-a-date":           "not-a-date",
	}
	for input, want := range tests {
		if got := formatDate(input); got != want {
			t.Errorf("formatDate(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPrintBannerIncludesBuildInformation(t *testing.T) {
	t.Setenv("OCTOPUS_DEBUG", "true")
	oldVersion, oldCommit, oldBuildTime, oldAuthor := Version, Commit, BuildTime, Author
	Version, Commit, BuildTime, Author = "1.2.3", "1234567890", "2026-07-14", "tester"
	t.Cleanup(func() {
		Version, Commit, BuildTime, Author = oldVersion, oldCommit, oldBuildTime, oldAuthor
	})

	output := captureStdout(t, PrintBanner)
	for _, text := range []string{"Debug", "1.2.3", "12345678", "2026-07-14 00:00", "tester"} {
		if !strings.Contains(output, text) {
			t.Errorf("PrintBanner() output does not contain %q", text)
		}
	}
}

func resetConfigState(t *testing.T) {
	t.Helper()
	viper.Reset()
	currentConfig.Store(Default())
	t.Cleanup(func() {
		viper.Reset()
		currentConfig.Store(Default())
	})
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = writer
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	os.Stdout = oldStdout
	t.Cleanup(func() { os.Stdout = oldStdout })

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}
	return string(output)
}
