package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"golang.org/x/net/proxy"
)

type customProxyClientEntry struct {
	client   *http.Client
	lastUsed time.Time
}

const (
	customProxyClientTTL        = 30 * time.Minute
	customProxyClientMaxEntries = 128
	customProxyClientSweepEvery = 64
)

var (
	systemDirectClient          *http.Client
	systemProxyClient           *http.Client
	systemProxyURL              string
	customProxyClients          = make(map[string]customProxyClientEntry)
	customProxyClientNow        = time.Now
	customProxyClientOperations uint64
	clientLock                  sync.RWMutex
)

// GetHTTPClientSystemProxy returns a cached http.Client.
// - useProxy=false: bypass proxy
// - useProxy=true: use proxy settings from system/app settings (setting key: proxy_url)
func GetHTTPClientSystemProxy(useProxy bool) (*http.Client, error) {
	if useProxy {
		currentProxyURL, err := op.SettingGetString(model.SettingKeyProxyURL)
		if err != nil {
			return nil, err
		}
		if currentProxyURL == "" {
			return nil, fmt.Errorf("proxy url is empty")
		}

		clientLock.RLock()
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			clientLock.RUnlock()
			return systemProxyClient, nil
		}
		clientLock.RUnlock()

		clientLock.Lock()
		defer clientLock.Unlock()

		// Re-check after acquiring write lock.
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			return systemProxyClient, nil
		}

		client, err := newHTTPClientCustomProxy(currentProxyURL)
		if err != nil {
			return nil, err
		}
		systemProxyClient = client
		systemProxyURL = currentProxyURL
		return systemProxyClient, nil
	}

	clientLock.RLock()
	if !useProxy && systemDirectClient != nil {
		clientLock.RUnlock()
		return systemDirectClient, nil
	}
	clientLock.RUnlock()

	clientLock.Lock()
	defer clientLock.Unlock()

	if systemDirectClient != nil {
		return systemDirectClient, nil
	}
	client, err := newHTTPClientNoProxy()
	if err != nil {
		return nil, err
	}
	systemDirectClient = client
	return systemDirectClient, nil
}

// GetHTTPClientCustomProxy 返回按代理地址复用的 http.Client。
// proxyURL supports: http, https, socks, socks5
func GetHTTPClientCustomProxy(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, fmt.Errorf("proxy url is empty")
	}

	clientLock.Lock()
	defer clientLock.Unlock()
	now := customProxyClientNow()
	customProxyClientOperations++
	if customProxyClientOperations%customProxyClientSweepEvery == 0 {
		sweepExpiredCustomProxyClientsLocked(now)
	}

	if entry, ok := customProxyClients[proxyURL]; ok {
		if !customProxyClientExpired(entry.lastUsed, now) {
			entry.lastUsed = now
			customProxyClients[proxyURL] = entry
			return entry.client, nil
		}
		delete(customProxyClients, proxyURL)
		closeCustomProxyClient(entry)
	}
	client, err := newHTTPClientCustomProxy(proxyURL)
	if err != nil {
		return nil, err
	}

	if len(customProxyClients) >= customProxyClientMaxEntries {
		sweepExpiredCustomProxyClientsLocked(now)
	}
	for len(customProxyClients) >= customProxyClientMaxEntries {
		evictOldestCustomProxyClientLocked()
	}
	customProxyClients[proxyURL] = customProxyClientEntry{client: client, lastUsed: now}
	return client, nil
}

// InvalidateCustomProxyClient removes a cached client for the supplied proxy
// URL and closes its idle connections. Leading/trailing whitespace is ignored
// in the same way as GetHTTPClientCustomProxy.
func InvalidateCustomProxyClient(proxyURL string) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return
	}

	clientLock.Lock()
	entry, ok := customProxyClients[proxyURL]
	if ok {
		delete(customProxyClients, proxyURL)
	}
	clientLock.Unlock()

	if ok {
		closeCustomProxyClient(entry)
	}
}

// InvalidateAllCustomProxyClients clears all channel-specific proxy clients
// and closes their idle connections.
func InvalidateAllCustomProxyClients() {
	clientLock.Lock()
	entries := make([]customProxyClientEntry, 0, len(customProxyClients))
	for _, entry := range customProxyClients {
		entries = append(entries, entry)
	}
	customProxyClients = make(map[string]customProxyClientEntry)
	customProxyClientOperations = 0
	clientLock.Unlock()

	for _, entry := range entries {
		closeCustomProxyClient(entry)
	}
}

func sweepExpiredCustomProxyClientsLocked(now time.Time) {
	for proxyURL, entry := range customProxyClients {
		if customProxyClientExpired(entry.lastUsed, now) {
			delete(customProxyClients, proxyURL)
			closeCustomProxyClient(entry)
		}
	}
}

func evictOldestCustomProxyClientLocked() {
	oldestURL := ""
	var oldest time.Time
	found := false
	for proxyURL, entry := range customProxyClients {
		if !found || entry.lastUsed.Before(oldest) {
			oldestURL = proxyURL
			oldest = entry.lastUsed
			found = true
		}
	}
	if !found {
		return
	}
	entry := customProxyClients[oldestURL]
	delete(customProxyClients, oldestURL)
	closeCustomProxyClient(entry)
}

func customProxyClientExpired(lastUsed, now time.Time) bool {
	if lastUsed.IsZero() {
		return true
	}
	return !now.Before(lastUsed.Add(customProxyClientTTL))
}

func closeCustomProxyClient(entry customProxyClientEntry) {
	if entry.client != nil {
		entry.client.CloseIdleConnections()
	}
}

func clonedDefaultTransport() (*http.Transport, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not *http.Transport")
	}
	return transport.Clone(), nil
}

func newHTTPTransportWithTimeouts() (*http.Transport, error) {
	cloned, err := clonedDefaultTransport()
	if err != nil {
		return nil, err
	}

	// Apply relay timeout configuration from global config
	cfg := conf.Current()

	// Set dial timeout (TCP + TLS handshake)
	if cfg.Relay.DialTimeoutSeconds > 0 {
		dialer := &net.Dialer{
			Timeout:   time.Duration(cfg.Relay.DialTimeoutSeconds) * time.Second,
			KeepAlive: 30 * time.Second,
		}
		cloned.DialContext = dialer.DialContext
	}

	// Set response header timeout
	if cfg.Relay.ResponseHeaderTimeoutSeconds > 0 {
		cloned.ResponseHeaderTimeout = time.Duration(cfg.Relay.ResponseHeaderTimeoutSeconds) * time.Second
	}

	return cloned, nil
}

func newHTTPClientNoProxy() (*http.Client, error) {
	cloned, err := newHTTPTransportWithTimeouts()
	if err != nil {
		return nil, err
	}
	cloned.Proxy = nil
	return &http.Client{Transport: &userAgentTransport{base: cloned}}, nil
}

func newHTTPClientCustomProxy(proxyURLStr string) (*http.Client, error) {
	cloned, err := newHTTPTransportWithTimeouts()
	if err != nil {
		return nil, err
	}

	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}

	switch proxyURL.Scheme {
	case "http", "https":
		cloned.Proxy = http.ProxyURL(proxyURL)
	case "socks", "socks5":
		socksDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("invalid socks proxy: %w", err)
		}
		cloned.Proxy = nil
		// For SOCKS proxy, wrap the dialer to preserve timeout settings
		cfg := conf.Current()
		dialTimeout := 30 * time.Second // Go default
		if cfg.Relay.DialTimeoutSeconds > 0 {
			dialTimeout = time.Duration(cfg.Relay.DialTimeoutSeconds) * time.Second
		}
		cloned.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Apply timeout to the SOCKS dial
			dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			// proxy.Dialer doesn't support context, so we use a goroutine with timeout
			type result struct {
				conn net.Conn
				err  error
			}
			ch := make(chan result, 1)
			go func() {
				conn, err := socksDialer.Dial(network, addr)
				ch <- result{conn, err}
			}()
			select {
			case res := <-ch:
				return res.conn, res.err
			case <-dialCtx.Done():
				return nil, dialCtx.Err()
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
	}

	return &http.Client{Transport: &userAgentTransport{base: cloned}}, nil
}

// userAgentTransport wraps an http.RoundTripper and injects a default User-Agent
// only when the request does not already specify one. This lets a channel override
// the outbound User-Agent (via its UserAgent field / custom headers) for upstreams
// that gate on client identity (e.g. relays that only allow Claude Code clients),
// while still shielding requests that don't set a UA from SDK-fingerprint blocking.
type userAgentTransport struct {
	base http.RoundTripper
}

// defaultOutboundUserAgent 是未显式指定 UA 时的兜底值：常见 Chrome UA，避免被上游按脚本/SDK 特征封禁。
const defaultOutboundUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	reqClone := req.Clone(req.Context())

	// 渠道已显式设置 User-Agent（渠道 UA 字段或自定义头）时保留其值；否则注入默认浏览器 UA。
	if reqClone.Header.Get("User-Agent") == "" {
		reqClone.Header.Set("User-Agent", defaultOutboundUserAgent)
	}

	return t.base.RoundTrip(reqClone)
}

func (t *userAgentTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
