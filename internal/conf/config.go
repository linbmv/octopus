package conf

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/spf13/viper"
)

type Server struct {
	Host                string   `mapstructure:"host"`
	Port                int      `mapstructure:"port"`
	TrustedProxies      []string `mapstructure:"trusted_proxies"`
	SessionCookieSecure string   `mapstructure:"session_cookie_secure"`
}

type Log struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type Database struct {
	Type string `mapstructure:"type"`
	Path string `mapstructure:"path"`
}

type JWT struct {
	DefaultExpiryMinutes int `mapstructure:"default_expiry_minutes"`
	MaxExpiryDays        int `mapstructure:"max_expiry_days"`
}

type Relay struct {
	// NonStreamTimeoutSeconds bounds the complete upstream lifecycle for a
	// non-streaming relay request, including retries. Zero disables the guard.
	NonStreamTimeoutSeconds        int `mapstructure:"non_stream_timeout_seconds"`
	StreamFirstEventTimeoutSeconds int `mapstructure:"stream_first_event_timeout_seconds"`
	StreamIdleTimeoutSeconds       int `mapstructure:"stream_idle_timeout_seconds"`
	// NonStreamAttemptTimeoutSeconds bounds one non-streaming upstream attempt
	// while waiting for response headers, so a hung channel yields to remaining
	// failover candidates instead of consuming the whole request budget. It is
	// only applied when another candidate exists; the last candidate keeps the
	// full NonStreamTimeoutSeconds budget. Zero disables the guard.
	NonStreamAttemptTimeoutSeconds int `mapstructure:"non_stream_attempt_timeout_seconds"`
	// StreamColdStartFirstEventTimeoutSeconds bounds the wait for the first
	// stream event on channels without adaptive health samples when another
	// failover candidate exists. Zero disables the cold-start override.
	StreamColdStartFirstEventTimeoutSeconds int   `mapstructure:"stream_cold_start_first_event_timeout_seconds"`
	MaxUpstreamAttempts                     int   `mapstructure:"max_upstream_attempts"`
	StreamFirstEventBudgetSeconds           int   `mapstructure:"stream_first_event_budget_seconds"`
	MaxJSONRequestBytes                     int64 `mapstructure:"max_json_request_bytes"`
	MaxImageRequestBytes                    int64 `mapstructure:"max_image_request_bytes"`
	MaxNonStreamResponseBytes               int64 `mapstructure:"max_non_stream_response_bytes"`
	// DialTimeoutSeconds bounds the TCP+TLS handshake phase for establishing
	// the upstream connection. Zero uses Go's default dialer timeout (30s).
	DialTimeoutSeconds int `mapstructure:"dial_timeout_seconds"`
	// ResponseHeaderTimeoutSeconds bounds the wait for upstream response headers
	// after the request has been fully written. Zero disables the guard.
	ResponseHeaderTimeoutSeconds int `mapstructure:"response_header_timeout_seconds"`
}

type Metrics struct {
	Enabled     bool     `mapstructure:"enabled"`
	Host        string   `mapstructure:"host"`
	Port        int      `mapstructure:"port"`
	BearerToken string   `mapstructure:"bearer_token"`
	Allowlist   []string `mapstructure:"allowlist"`
}

type Tracing struct {
	Enabled     bool    `mapstructure:"enabled"`
	Endpoint    string  `mapstructure:"endpoint"`
	Insecure    bool    `mapstructure:"insecure"`
	SampleRatio float64 `mapstructure:"sample_ratio"`
}

type Observability struct {
	Metrics Metrics `mapstructure:"metrics"`
	Tracing Tracing `mapstructure:"tracing"`
}

type WebAuthn struct {
	Enabled       bool     `mapstructure:"enabled"`
	RPID          string   `mapstructure:"rp_id"`
	RPDisplayName string   `mapstructure:"rp_display_name"`
	RPOrigins     []string `mapstructure:"rp_origins"`
}

// CapabilityProbe is intentionally disabled by default because probes can
// create billable provider traffic. CostPerProbeUSD is the operator's
// conservative reservation for one request; MaxBatchCostUSD is enforced before
// any batch is accepted.
type CapabilityProbe struct {
	Enabled           bool    `mapstructure:"enabled"`
	TTLSeconds        int     `mapstructure:"ttl_seconds"`
	RequestsPerMinute int     `mapstructure:"requests_per_minute"`
	MaxConcurrency    int     `mapstructure:"max_concurrency"`
	QueueDepth        int     `mapstructure:"queue_depth"`
	TimeoutSeconds    int     `mapstructure:"timeout_seconds"`
	MaxOutputTokens   int     `mapstructure:"max_output_tokens"`
	CostPerProbeUSD   float64 `mapstructure:"cost_per_probe_usd"`
	MaxBatchCostUSD   float64 `mapstructure:"max_batch_cost_usd"`
	MaxTotalCostUSD   float64 `mapstructure:"max_total_cost_usd"`
}

// SelfHealing controls runtime evidence collection and the future diagnostic
// worker. It is disabled by default because even redacted baseline capture is
// coupled to real provider traffic and must be an explicit operator choice.
type SelfHealing struct {
	Enabled                 bool                  `mapstructure:"enabled"`
	CaptureSuccessBaselines bool                  `mapstructure:"capture_success_baselines"`
	BaselineTTLSeconds      int                   `mapstructure:"baseline_ttl_seconds"`
	SentinelIntervalSeconds int                   `mapstructure:"sentinel_interval_seconds"`
	FailureThreshold        int                   `mapstructure:"failure_threshold"`
	FailureWindowSeconds    int                   `mapstructure:"failure_window_seconds"`
	Diagnostic              SelfHealingDiagnostic `mapstructure:"diagnostic"`
}

type SelfHealingDiagnostic struct {
	MaxVariants       int     `mapstructure:"max_variants"`
	MaxConcurrency    int     `mapstructure:"max_concurrency"`
	QueueDepth        int     `mapstructure:"queue_depth"`
	RequestsPerMinute int     `mapstructure:"requests_per_minute"`
	TimeoutSeconds    int     `mapstructure:"timeout_seconds"`
	SessionTTLSeconds int     `mapstructure:"session_ttl_seconds"`
	CostPerRequestUSD float64 `mapstructure:"cost_per_request_usd"`
	MaxBatchCostUSD   float64 `mapstructure:"max_batch_cost_usd"`
	MaxTotalCostUSD   float64 `mapstructure:"max_total_cost_usd"`
	// ExtraUserAgents and ExtraHeaders extend the built-in client-fingerprint
	// candidates without a redeploy when upstream clients ship new versions.
	// ExtraHeaders entries use "name: value" form; protected auth headers are
	// dropped during variant normalization.
	ExtraUserAgents []string `mapstructure:"extra_user_agents"`
	ExtraHeaders    []string `mapstructure:"extra_headers"`
}

type Config struct {
	Server          Server          `mapstructure:"server"`
	Log             Log             `mapstructure:"log"`
	Database        Database        `mapstructure:"database"`
	JWT             JWT             `mapstructure:"jwt"`
	Relay           Relay           `mapstructure:"relay"`
	Observability   Observability   `mapstructure:"observability"`
	WebAuthn        WebAuthn        `mapstructure:"webauthn"`
	CapabilityProbe CapabilityProbe `mapstructure:"capability_probe"`
	SelfHealing     SelfHealing     `mapstructure:"self_healing"`
}

var (
	currentConfig    atomic.Value
	loadedPathMu     sync.RWMutex
	loadedConfigPath string
)

func init() {
	config := Default()
	currentConfig.Store(config)
}

func Default() Config {
	v := viper.New()
	setDefaultsFor(v)
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		panic(fmt.Sprintf("decode built-in default config: %v", err))
	}
	return config
}

func Current() Config {
	return cloneConfig(currentConfig.Load().(Config))
}

func Set(config Config) error {
	if err := Validate(config); err != nil {
		return err
	}
	currentConfig.Store(cloneConfig(config))
	return nil
}

func cloneConfig(config Config) Config {
	config.Server.TrustedProxies = slices.Clone(config.Server.TrustedProxies)
	config.Observability.Metrics.Allowlist = slices.Clone(config.Observability.Metrics.Allowlist)
	config.WebAuthn.RPOrigins = slices.Clone(config.WebAuthn.RPOrigins)
	return config
}

func LoadedPath() string {
	loadedPathMu.RLock()
	defer loadedPathMu.RUnlock()
	return loadedConfigPath
}

func setLoadedPath(path string) {
	loadedPathMu.Lock()
	loadedConfigPath = path
	loadedPathMu.Unlock()
}

func Load(path string) error {
	if path != "" {
		viper.SetConfigFile(path)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("json")
		viper.AddConfigPath("data")
	}

	viper.AutomaticEnv()
	viper.SetEnvPrefix(APP_NAME)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	setDefaults()

	if err := viper.ReadInConfig(); err == nil {
		log.Infof("Using config file: %s", viper.ConfigFileUsed())
	} else {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Infof("Config file not found, creating default config")
			if err := os.MkdirAll("data", 0755); err != nil {
				return fmt.Errorf("create default config directory: %w", err)
			}
			if err := viper.SafeWriteConfigAs("data/config.json"); err != nil {
				return fmt.Errorf("write default config: %w", err)
			}
		} else {
			return fmt.Errorf("error reading config file: %w", err)
		}
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return fmt.Errorf("unable to decode config into struct: %w", err)
	}
	if err := Set(config); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	usedPath := viper.ConfigFileUsed()
	if usedPath == "" {
		usedPath = "data/config.json"
	}
	setLoadedPath(usedPath)
	return nil
}

func setDefaults() {
	setDefaultsFor(viper.GetViper())
}

func setDefaultsFor(v *viper.Viper) {
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.trusted_proxies", []string{})
	v.SetDefault("server.session_cookie_secure", "auto")
	v.SetDefault("database.type", "sqlite")
	v.SetDefault("database.path", "data/data.db")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("jwt.default_expiry_minutes", 15)
	v.SetDefault("jwt.max_expiry_days", 30)
	v.SetDefault("relay.non_stream_timeout_seconds", 600)
	v.SetDefault("relay.stream_first_event_timeout_seconds", 600)
	v.SetDefault("relay.stream_idle_timeout_seconds", 600)
	v.SetDefault("relay.non_stream_attempt_timeout_seconds", 60)
	v.SetDefault("relay.stream_cold_start_first_event_timeout_seconds", 30)
	v.SetDefault("relay.max_upstream_attempts", 8)
	v.SetDefault("relay.stream_first_event_budget_seconds", 120)
	v.SetDefault("relay.max_json_request_bytes", 32<<20)
	v.SetDefault("relay.max_image_request_bytes", 64<<20)
	v.SetDefault("relay.max_non_stream_response_bytes", 64<<20)
	v.SetDefault("relay.dial_timeout_seconds", 10)
	v.SetDefault("relay.response_header_timeout_seconds", 30)
	v.SetDefault("observability.metrics.enabled", false)
	v.SetDefault("observability.metrics.host", "127.0.0.1")
	v.SetDefault("observability.metrics.port", 9090)
	v.SetDefault("observability.metrics.bearer_token", "")
	v.SetDefault("observability.metrics.allowlist", []string{})
	v.SetDefault("observability.tracing.enabled", false)
	v.SetDefault("observability.tracing.endpoint", "localhost:4318")
	v.SetDefault("observability.tracing.insecure", true)
	v.SetDefault("observability.tracing.sample_ratio", 0.01)
	v.SetDefault("webauthn.enabled", false)
	v.SetDefault("webauthn.rp_id", "")
	v.SetDefault("webauthn.rp_display_name", APP_NAME)
	v.SetDefault("webauthn.rp_origins", []string{})
	v.SetDefault("capability_probe.enabled", false)
	v.SetDefault("capability_probe.ttl_seconds", 86400)
	v.SetDefault("capability_probe.requests_per_minute", 6)
	v.SetDefault("capability_probe.max_concurrency", 1)
	v.SetDefault("capability_probe.queue_depth", 64)
	v.SetDefault("capability_probe.timeout_seconds", 30)
	v.SetDefault("capability_probe.max_output_tokens", 8)
	v.SetDefault("capability_probe.cost_per_probe_usd", 0.001)
	v.SetDefault("capability_probe.max_batch_cost_usd", 0.05)
	v.SetDefault("capability_probe.max_total_cost_usd", 0.25)
	v.SetDefault("self_healing.enabled", false)
	v.SetDefault("self_healing.capture_success_baselines", false)
	v.SetDefault("self_healing.baseline_ttl_seconds", 86400)
	v.SetDefault("self_healing.sentinel_interval_seconds", 1800)
	v.SetDefault("self_healing.failure_threshold", 3)
	v.SetDefault("self_healing.failure_window_seconds", 300)
	v.SetDefault("self_healing.diagnostic.max_variants", 8)
	v.SetDefault("self_healing.diagnostic.max_concurrency", 1)
	v.SetDefault("self_healing.diagnostic.queue_depth", 16)
	v.SetDefault("self_healing.diagnostic.requests_per_minute", 6)
	v.SetDefault("self_healing.diagnostic.timeout_seconds", 30)
	v.SetDefault("self_healing.diagnostic.session_ttl_seconds", 300)
	v.SetDefault("self_healing.diagnostic.cost_per_request_usd", 0.001)
	v.SetDefault("self_healing.diagnostic.max_batch_cost_usd", 0.01)
	v.SetDefault("self_healing.diagnostic.max_total_cost_usd", 0.05)
}

func Validate(config Config) error {
	if config.Server.Port < 0 || config.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 0 and 65535")
	}
	for _, proxy := range config.Server.TrustedProxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			return fmt.Errorf("server.trusted_proxies entries must not be empty")
		}
		if net.ParseIP(strings.Trim(proxy, "[]")) == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				return fmt.Errorf("server.trusted_proxies entry %q must be an IP address or CIDR", proxy)
			}
		}
	}
	if config.Server.SessionCookieSecure != "auto" && config.Server.SessionCookieSecure != "always" {
		return fmt.Errorf("server.session_cookie_secure must be auto or always")
	}
	switch config.Database.Type {
	case "sqlite", "mysql", "postgres", "postgresql":
	default:
		return fmt.Errorf("unsupported database type: %s", config.Database.Type)
	}
	if config.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	switch strings.ToLower(config.Log.Level) {
	case "debug", "info", "warn", "warning", "error", "dpanic", "panic", "fatal":
	default:
		return fmt.Errorf("unsupported log level: %s", config.Log.Level)
	}
	if config.Log.Format != "json" && config.Log.Format != "console" {
		return fmt.Errorf("log.format must be json or console")
	}
	if config.JWT.DefaultExpiryMinutes <= 0 || config.JWT.MaxExpiryDays <= 0 {
		return fmt.Errorf("JWT expiry values must be positive")
	}
	if config.Relay.NonStreamTimeoutSeconds < 0 || config.Relay.NonStreamTimeoutSeconds > 24*60*60 {
		return fmt.Errorf("relay.non_stream_timeout_seconds must be between 0 and 86400")
	}
	if config.Relay.StreamFirstEventTimeoutSeconds < 0 || config.Relay.StreamFirstEventTimeoutSeconds > 24*60*60 {
		return fmt.Errorf("relay.stream_first_event_timeout_seconds must be between 0 and 86400")
	}
	if config.Relay.StreamIdleTimeoutSeconds < 0 || config.Relay.StreamIdleTimeoutSeconds > 24*60*60 {
		return fmt.Errorf("relay.stream_idle_timeout_seconds must be between 0 and 86400")
	}
	if config.Relay.NonStreamAttemptTimeoutSeconds < 0 || config.Relay.NonStreamAttemptTimeoutSeconds > 24*60*60 {
		return fmt.Errorf("relay.non_stream_attempt_timeout_seconds must be between 0 and 86400")
	}
	if config.Relay.StreamColdStartFirstEventTimeoutSeconds < 0 || config.Relay.StreamColdStartFirstEventTimeoutSeconds > 24*60*60 {
		return fmt.Errorf("relay.stream_cold_start_first_event_timeout_seconds must be between 0 and 86400")
	}
	if config.Relay.MaxUpstreamAttempts < 0 || config.Relay.MaxUpstreamAttempts > 1000 {
		return fmt.Errorf("relay.max_upstream_attempts must be between 0 and 1000")
	}
	if config.Relay.StreamFirstEventBudgetSeconds < 0 || config.Relay.StreamFirstEventBudgetSeconds > 24*60*60 {
		return fmt.Errorf("relay.stream_first_event_budget_seconds must be between 0 and 86400")
	}
	if config.Relay.DialTimeoutSeconds < 0 || config.Relay.DialTimeoutSeconds > 300 {
		return fmt.Errorf("relay.dial_timeout_seconds must be between 0 and 300")
	}
	if config.Relay.ResponseHeaderTimeoutSeconds < 0 || config.Relay.ResponseHeaderTimeoutSeconds > 600 {
		return fmt.Errorf("relay.response_header_timeout_seconds must be between 0 and 600")
	}
	const maxConfiguredBodyBytes = int64(1 << 30)
	for name, value := range map[string]int64{
		"relay.max_json_request_bytes":        config.Relay.MaxJSONRequestBytes,
		"relay.max_image_request_bytes":       config.Relay.MaxImageRequestBytes,
		"relay.max_non_stream_response_bytes": config.Relay.MaxNonStreamResponseBytes,
	} {
		if value <= 0 || value > maxConfiguredBodyBytes {
			return fmt.Errorf("%s must be between 1 and %d", name, maxConfiguredBodyBytes)
		}
	}
	if config.Observability.Tracing.SampleRatio < 0 || config.Observability.Tracing.SampleRatio > 1 {
		return fmt.Errorf("observability.tracing.sample_ratio must be between 0 and 1")
	}
	if config.Observability.Tracing.Enabled && config.Observability.Tracing.Endpoint == "" {
		return fmt.Errorf("observability.tracing.endpoint is required when tracing is enabled")
	}
	metricsConfig := config.Observability.Metrics
	if metricsConfig.Port < 0 || metricsConfig.Port > 65535 {
		return fmt.Errorf("observability.metrics.port must be between 0 and 65535")
	}
	if len(metricsConfig.BearerToken) > 512 || strings.ContainsAny(metricsConfig.BearerToken, "\r\n") {
		return fmt.Errorf("observability.metrics.bearer_token must be at most 512 bytes without line breaks")
	}
	if metricsConfig.BearerToken != "" && len(metricsConfig.BearerToken) < 16 {
		return fmt.Errorf("observability.metrics.bearer_token must contain at least 16 bytes when configured")
	}
	for _, entry := range metricsConfig.Allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return fmt.Errorf("observability.metrics.allowlist entries must not be empty")
		}
		if net.ParseIP(strings.Trim(entry, "[]")) == nil {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return fmt.Errorf("observability.metrics.allowlist entry %q must be an IP address or CIDR", entry)
			}
		}
	}
	if metricsConfig.Enabled {
		if strings.TrimSpace(metricsConfig.Host) == "" {
			return fmt.Errorf("observability.metrics.host is required when metrics is enabled")
		}
		if metricsConfig.Port == 0 {
			return fmt.Errorf("observability.metrics.port must be non-zero when metrics is enabled")
		}
		if !isLoopbackHost(metricsConfig.Host) && metricsConfig.BearerToken == "" {
			return fmt.Errorf("observability.metrics.bearer_token is required for a non-loopback metrics host")
		}
	}
	if config.WebAuthn.Enabled {
		if strings.TrimSpace(config.WebAuthn.RPID) == "" {
			return fmt.Errorf("webauthn.rp_id is required when WebAuthn is enabled")
		}
		if strings.TrimSpace(config.WebAuthn.RPDisplayName) == "" {
			return fmt.Errorf("webauthn.rp_display_name is required when WebAuthn is enabled")
		}
		if len(config.WebAuthn.RPOrigins) == 0 {
			return fmt.Errorf("webauthn.rp_origins must contain at least one origin when WebAuthn is enabled")
		}
		for _, origin := range config.WebAuthn.RPOrigins {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("webauthn.rp_origins entry %q must be an origin without path, query, fragment, or user info", origin)
			}
			if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
				return fmt.Errorf("webauthn.rp_origins entry %q must use https (http is allowed only for loopback development)", origin)
			}
		}
	}
	probe := config.CapabilityProbe
	if probe.TTLSeconds < 60 || probe.TTLSeconds > 90*24*60*60 {
		return fmt.Errorf("capability_probe.ttl_seconds must be between 60 and 7776000")
	}
	if probe.RequestsPerMinute <= 0 || probe.RequestsPerMinute > 6000 {
		return fmt.Errorf("capability_probe.requests_per_minute must be between 1 and 6000")
	}
	if probe.MaxConcurrency <= 0 || probe.MaxConcurrency > 32 {
		return fmt.Errorf("capability_probe.max_concurrency must be between 1 and 32")
	}
	if probe.QueueDepth <= 0 || probe.QueueDepth > 10000 {
		return fmt.Errorf("capability_probe.queue_depth must be between 1 and 10000")
	}
	if probe.TimeoutSeconds <= 0 || probe.TimeoutSeconds > 300 {
		return fmt.Errorf("capability_probe.timeout_seconds must be between 1 and 300")
	}
	if probe.MaxOutputTokens <= 0 || probe.MaxOutputTokens > 64 {
		return fmt.Errorf("capability_probe.max_output_tokens must be between 1 and 64")
	}
	if probe.CostPerProbeUSD <= 0 || probe.CostPerProbeUSD > 10 {
		return fmt.Errorf("capability_probe.cost_per_probe_usd must be greater than 0 and at most 10")
	}
	if probe.MaxBatchCostUSD <= 0 || probe.MaxBatchCostUSD > 1000 {
		return fmt.Errorf("capability_probe.max_batch_cost_usd must be greater than 0 and at most 1000")
	}
	if probe.MaxTotalCostUSD <= 0 || probe.MaxTotalCostUSD > 10000 {
		return fmt.Errorf("capability_probe.max_total_cost_usd must be greater than 0 and at most 10000")
	}
	if probe.MaxBatchCostUSD > probe.MaxTotalCostUSD {
		return fmt.Errorf("capability_probe.max_batch_cost_usd must not exceed max_total_cost_usd")
	}
	if config.SelfHealing.BaselineTTLSeconds < 60 || config.SelfHealing.BaselineTTLSeconds > 90*24*60*60 {
		return fmt.Errorf("self_healing.baseline_ttl_seconds must be between 60 and 7776000")
	}
	if config.SelfHealing.SentinelIntervalSeconds < 60 || config.SelfHealing.SentinelIntervalSeconds > 24*60*60 {
		return fmt.Errorf("self_healing.sentinel_interval_seconds must be between 60 and 86400")
	}
	if config.SelfHealing.FailureThreshold < 1 || config.SelfHealing.FailureThreshold > 100 {
		return fmt.Errorf("self_healing.failure_threshold must be between 1 and 100")
	}
	if config.SelfHealing.FailureWindowSeconds < 60 || config.SelfHealing.FailureWindowSeconds > 24*60*60 {
		return fmt.Errorf("self_healing.failure_window_seconds must be between 60 and 86400")
	}
	diagnostic := config.SelfHealing.Diagnostic
	if diagnostic.MaxVariants < 1 || diagnostic.MaxVariants > 16 {
		return fmt.Errorf("self_healing.diagnostic.max_variants must be between 1 and 16")
	}
	if diagnostic.MaxConcurrency < 1 || diagnostic.MaxConcurrency > 8 {
		return fmt.Errorf("self_healing.diagnostic.max_concurrency must be between 1 and 8")
	}
	if diagnostic.QueueDepth < 1 || diagnostic.QueueDepth > 256 {
		return fmt.Errorf("self_healing.diagnostic.queue_depth must be between 1 and 256")
	}
	if diagnostic.RequestsPerMinute < 1 || diagnostic.RequestsPerMinute > 600 {
		return fmt.Errorf("self_healing.diagnostic.requests_per_minute must be between 1 and 600")
	}
	if diagnostic.TimeoutSeconds < 1 || diagnostic.TimeoutSeconds > 300 {
		return fmt.Errorf("self_healing.diagnostic.timeout_seconds must be between 1 and 300")
	}
	if diagnostic.SessionTTLSeconds < 60 || diagnostic.SessionTTLSeconds > 3600 {
		return fmt.Errorf("self_healing.diagnostic.session_ttl_seconds must be between 60 and 3600")
	}
	if diagnostic.CostPerRequestUSD <= 0 || diagnostic.CostPerRequestUSD > 10 {
		return fmt.Errorf("self_healing.diagnostic.cost_per_request_usd must be greater than 0 and at most 10")
	}
	if diagnostic.MaxBatchCostUSD <= 0 || diagnostic.MaxBatchCostUSD > 1000 {
		return fmt.Errorf("self_healing.diagnostic.max_batch_cost_usd must be greater than 0 and at most 1000")
	}
	if diagnostic.MaxTotalCostUSD <= 0 || diagnostic.MaxTotalCostUSD > 10000 {
		return fmt.Errorf("self_healing.diagnostic.max_total_cost_usd must be greater than 0 and at most 10000")
	}
	if diagnostic.MaxBatchCostUSD > diagnostic.MaxTotalCostUSD {
		return fmt.Errorf("self_healing.diagnostic.max_batch_cost_usd must not exceed max_total_cost_usd")
	}
	if len(diagnostic.ExtraUserAgents) > 8 || len(diagnostic.ExtraHeaders) > 8 {
		return fmt.Errorf("self_healing.diagnostic.extra_user_agents and extra_headers each allow at most 8 entries")
	}
	for _, header := range diagnostic.ExtraHeaders {
		name, _, found := strings.Cut(header, ":")
		if !found || strings.TrimSpace(name) == "" {
			return fmt.Errorf("self_healing.diagnostic.extra_headers entries must use \"name: value\" form, got %q", header)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
