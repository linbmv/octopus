package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
)

// TestNewHTTPTransportWithTimeouts_AppliesDialTimeout verifies that the dial timeout
// from config is correctly applied to the HTTP transport.
func TestNewHTTPTransportWithTimeouts_AppliesDialTimeout(t *testing.T) {
	// Save original config and restore after test
	originalConfig := conf.Current()
	defer conf.Set(originalConfig)

	// Set a specific dial timeout
	cfg := originalConfig
	cfg.Relay.DialTimeoutSeconds = 2
	conf.Set(cfg)

	transport, err := newHTTPTransportWithTimeouts()
	if err != nil {
		t.Fatalf("newHTTPTransportWithTimeouts failed: %v", err)
	}

	// Use a non-routable address (RFC 5737 TEST-NET-1): SYN packets are dropped,
	// so the TCP handshake can only end via the dialer timeout.
	addr := "192.0.2.1:81"

	start := time.Now()
	ctx := context.Background()
	conn, err := transport.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)

	if err == nil {
		conn.Close()
		t.Fatal("expected dial to timeout, but it succeeded")
	}

	// Verify timeout happened within reasonable bounds (2s ± 1s tolerance)
	if elapsed < 1*time.Second || elapsed > 4*time.Second {
		t.Errorf("dial timeout took %v, expected ~2s (config value)", elapsed)
	}
}

// TestNewHTTPTransportWithTimeouts_AppliesResponseHeaderTimeout verifies that
// the response header timeout from config is correctly applied.
func TestNewHTTPTransportWithTimeouts_AppliesResponseHeaderTimeout(t *testing.T) {
	// Save original config and restore after test
	originalConfig := conf.Current()
	defer conf.Set(originalConfig)

	// Set a specific response header timeout
	cfg := originalConfig
	cfg.Relay.ResponseHeaderTimeoutSeconds = 2
	conf.Set(cfg)

	transport, err := newHTTPTransportWithTimeouts()
	if err != nil {
		t.Fatalf("newHTTPTransportWithTimeouts failed: %v", err)
	}

	// Create a server that accepts connection but never sends response headers
	// until the request context is done (so server.Close() is not blocked).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	// Create HTTP client with our transport
	client := &http.Client{Transport: transport}

	// Try to make request - should timeout waiting for response headers
	start := time.Now()
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	if err == nil {
		resp.Body.Close()
		t.Fatal("expected response header timeout, but request succeeded")
	}

	// Verify the error is a timeout
	if !isTimeoutError(err) {
		t.Errorf("expected timeout error, got: %v", err)
	}

	// Verify timeout happened within reasonable bounds (2s ± 1s tolerance)
	if elapsed < 1*time.Second || elapsed > 4*time.Second {
		t.Errorf("response header timeout took %v, expected ~2s (config value)", elapsed)
	}
}

// TestNewHTTPTransportWithTimeouts_ZeroTimeoutDisablesGuard verifies that
// setting timeout to 0 disables the timeout guard.
func TestNewHTTPTransportWithTimeouts_ZeroTimeoutDisablesGuard(t *testing.T) {
	// Save original config and restore after test
	originalConfig := conf.Current()
	defer conf.Set(originalConfig)

	// Set timeouts to 0 (disabled)
	cfg := originalConfig
	cfg.Relay.DialTimeoutSeconds = 0
	cfg.Relay.ResponseHeaderTimeoutSeconds = 0
	conf.Set(cfg)

	transport, err := newHTTPTransportWithTimeouts()
	if err != nil {
		t.Fatalf("newHTTPTransportWithTimeouts failed: %v", err)
	}

	// When dial timeout is 0, DialContext should still be set (Go default 30s)
	// But ResponseHeaderTimeout should be 0 (disabled)
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("expected ResponseHeaderTimeout=0 when config is 0, got %v", transport.ResponseHeaderTimeout)
	}
}

// TestNewHTTPClientNoProxy_UsesTimeouts verifies that the no-proxy client
// correctly applies timeouts from config.
func TestNewHTTPClientNoProxy_UsesTimeouts(t *testing.T) {
	// Save original config and restore after test
	originalConfig := conf.Current()
	defer conf.Set(originalConfig)

	// Set specific timeouts
	cfg := originalConfig
	cfg.Relay.DialTimeoutSeconds = 5
	cfg.Relay.ResponseHeaderTimeoutSeconds = 10
	conf.Set(cfg)

	client, err := newHTTPClientNoProxy()
	if err != nil {
		t.Fatalf("newHTTPClientNoProxy failed: %v", err)
	}

	// Extract transport through the userAgentTransport wrapper
	uaTransport, ok := client.Transport.(*userAgentTransport)
	if !ok {
		t.Fatal("expected userAgentTransport")
	}

	transport, ok := uaTransport.base.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}

	// Verify timeouts are applied
	if transport.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("expected ResponseHeaderTimeout=10s, got %v", transport.ResponseHeaderTimeout)
	}

	// Verify DialContext is set (we can't easily test the timeout value directly)
	if transport.DialContext == nil {
		t.Error("expected DialContext to be set")
	}
}

// isTimeoutError checks if an error is a timeout error
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	// Check for net.Error with Timeout() == true
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}
	// Check for context.DeadlineExceeded
	if err == context.DeadlineExceeded {
		return true
	}
	return false
}
