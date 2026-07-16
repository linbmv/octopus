package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/gin-gonic/gin"
)

func TestGeminiContentActionOnlyAllowsContentActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []string{
		"/v1beta/models/gemini-1.5-flash:generateContent",
		"/v1beta/models/gemini-1.5-flash:streamGenerateContent",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			called := false
			r.POST("/v1beta/models/*action", geminiContentActionOnly(func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
			}
			if !called {
				t.Fatal("next handler was not called")
			}
		})
	}
}

func TestGeminiContentActionOnlyRejectsUnsupportedActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []string{
		"/v1beta/models/gemini-1.5-flash:countTokens",
		"/v1beta/models/gemini-1.5-flash:batchGenerateContent",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			called := false
			r.POST("/v1beta/models/*action", geminiContentActionOnly(func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
			if called {
				t.Fatal("next handler was called")
			}
		})
	}
}

func TestRegisterRelayRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerRelayRoutes(engine)

	want := map[string]bool{
		"POST /v1/chat/completions":   false,
		"POST /v1/responses":          false,
		"POST /v1/responses/compact":  false,
		"POST /v1/messages":           false,
		"POST /v1/embeddings":         false,
		"POST /v1/images/generations": false,
		"POST /v1/images/edits":       false,
		"POST /v1/images/variations":  false,
		"POST /v1beta/models/*action": false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("route %s was not registered", route)
		}
	}
}

func TestStartReturnsListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("listener.Close() error = %v", closeErr)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	oldConfig := conf.Current()
	config := oldConfig
	config.Server.Host = "127.0.0.1"
	config.Server.Port = port
	if err := conf.Set(config); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	t.Setenv("OCTOPUS_DEBUG", "false")
	t.Cleanup(func() {
		if err := conf.Set(oldConfig); err != nil {
			t.Errorf("restore configuration: %v", err)
		}
	})

	err = Start()
	if err == nil || !strings.Contains(err.Error(), "listen on 127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("Start() error = %v, want occupied-port error", err)
	}
}

func TestStartMetricsListenFailureReleasesApplicationListener(t *testing.T) {
	metricsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("metrics Listen() error = %v", err)
	}
	defer func() {
		if closeErr := metricsListener.Close(); closeErr != nil {
			t.Errorf("metrics listener close: %v", closeErr)
		}
	}()
	metricsPort := metricsListener.Addr().(*net.TCPAddr).Port
	appPort := reserveTCPPort(t)

	oldConfig := conf.Current()
	config := oldConfig
	config.Server.Host = "127.0.0.1"
	config.Server.Port = appPort
	config.Observability.Metrics.Enabled = true
	config.Observability.Metrics.Host = "127.0.0.1"
	config.Observability.Metrics.Port = metricsPort
	if err := conf.Set(config); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	t.Cleanup(func() {
		if err := conf.Set(oldConfig); err != nil {
			t.Errorf("restore configuration: %v", err)
		}
	})

	err = Start()
	if err == nil || !strings.Contains(err.Error(), "listen for metrics") {
		t.Fatalf("Start() error = %v, want metrics occupied-port error", err)
	}

	applicationListener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(appPort))
	if err != nil {
		t.Fatalf("application listener leaked after metrics bind failure: %v", err)
	}
	if err := applicationListener.Close(); err != nil {
		t.Fatalf("application listener close: %v", err)
	}
}

func TestStartAndClose(t *testing.T) {
	oldConfig := conf.Current()
	config := oldConfig
	config.Server.Host = "127.0.0.1"
	config.Server.Port = 0
	if err := conf.Set(config); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	t.Setenv("OCTOPUS_DEBUG", "true")
	t.Cleanup(func() {
		if err := conf.Set(oldConfig); err != nil {
			t.Errorf("restore configuration: %v", err)
		}
	})

	if err := Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	httpSrvMu.Lock()
	srv := httpSrv
	httpSrvMu.Unlock()
	if srv == nil {
		t.Fatal("Start() did not retain the HTTP server")
	}
	if srv.ReadHeaderTimeout != 5*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 1<<20 {
		t.Fatalf("HTTP limits = read-header %s, idle %s, max-header %d", srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if err := Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestStartExposesMetricsOnlyOnAuthenticatedDedicatedListener(t *testing.T) {
	appPort := reserveTCPPort(t)
	metricsPort := reserveTCPPort(t)
	const metricsToken = "0123456789abcdef0123456789abcdef"

	oldConfig := conf.Current()
	config := oldConfig
	config.Server.Host = "127.0.0.1"
	config.Server.Port = appPort
	config.Observability.Metrics.Enabled = true
	config.Observability.Metrics.Host = "127.0.0.1"
	config.Observability.Metrics.Port = metricsPort
	config.Observability.Metrics.BearerToken = metricsToken
	config.Observability.Tracing.Enabled = false
	if err := conf.Set(config); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	t.Setenv("OCTOPUS_DEBUG", "false")
	t.Cleanup(func() {
		if err := conf.Set(oldConfig); err != nil {
			t.Errorf("restore configuration: %v", err)
		}
	})

	if err := Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	publicResponse, err := http.Get("http://127.0.0.1:" + strconv.Itoa(appPort) + "/metrics")
	if err != nil {
		t.Fatalf("GET public /metrics error = %v", err)
	}
	if closeErr := publicResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close public response: %v", closeErr)
	}
	if publicResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("public metrics status = %d, want 404", publicResponse.StatusCode)
	}

	metricsURL := "http://127.0.0.1:" + strconv.Itoa(metricsPort) + "/metrics"
	unauthorizedResponse, err := http.Get(metricsURL)
	if err != nil {
		t.Fatalf("GET unauthenticated metrics error = %v", err)
	}
	if closeErr := unauthorizedResponse.Body.Close(); closeErr != nil {
		t.Fatalf("close unauthorized response: %v", closeErr)
	}
	if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status = %d, want 401", unauthorizedResponse.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, metricsURL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+metricsToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET authenticated metrics error = %v", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("response.Body.Close() error = %v", closeErr)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "octopus_http_requests_in_flight") {
		t.Fatal("metrics endpoint does not expose octopus HTTP metrics")
	}

	httpSrvMu.Lock()
	metricsServer := metricsSrv
	httpSrvMu.Unlock()
	if metricsServer == nil {
		t.Fatal("dedicated metrics server was not retained")
	}
	if metricsServer.ReadHeaderTimeout != 5*time.Second || metricsServer.IdleTimeout != 120*time.Second || metricsServer.MaxHeaderBytes != 1<<20 {
		t.Fatalf("metrics HTTP limits = read-header %s, idle %s, max-header %d", metricsServer.ReadHeaderTimeout, metricsServer.IdleTimeout, metricsServer.MaxHeaderBytes)
	}
}

func TestMetricsClientAllowlist(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		allowlist  []string
		want       bool
	}{
		{name: "empty allowlist", remoteAddr: "203.0.113.7:43123", want: true},
		{name: "exact IPv4", remoteAddr: "203.0.113.7:43123", allowlist: []string{"203.0.113.7"}, want: true},
		{name: "IPv4 CIDR", remoteAddr: "203.0.113.7:43123", allowlist: []string{"203.0.113.0/24"}, want: true},
		{name: "IPv4 rejected", remoteAddr: "198.51.100.4:43123", allowlist: []string{"203.0.113.0/24"}, want: false},
		{name: "IPv6 CIDR", remoteAddr: "[2001:db8::7]:43123", allowlist: []string{"2001:db8::/32"}, want: true},
		{name: "malformed remote", remoteAddr: "unknown", allowlist: []string{"127.0.0.1"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.RemoteAddr = test.remoteAddr
			if got := metricsClientAllowed(request, test.allowlist); got != test.want {
				t.Fatalf("metricsClientAllowed() = %t, want %t", got, test.want)
			}
		})
	}
	if metricsClientAllowed(nil, []string{"127.0.0.1"}) {
		t.Fatal("nil request passed a non-empty metrics allowlist")
	}
}

func TestMetricsServerEnforcesAllowlistBeforeBearer(t *testing.T) {
	token := strings.Repeat("test-", 8)
	handler := newMetricsServer(conf.Metrics{
		BearerToken: token,
		Allowlist:   []string{"203.0.113.0/24"},
	}).Handler

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "198.51.100.7:43123"
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("disallowed client status = %d, want 403", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "203.0.113.7:43123"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("allowed unauthenticated client status = %d, want 401", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "203.0.113.7:43123"
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("allowed authenticated client status = %d, want 200", response.Code)
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	return port
}
