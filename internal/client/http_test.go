package client

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetHTTPClientCacheForTest() {
	clientLock.Lock()
	entries := make([]customProxyClientEntry, 0, len(customProxyClients))
	for _, entry := range customProxyClients {
		entries = append(entries, entry)
	}
	systemDirectClient = nil
	systemProxyClient = nil
	systemProxyURL = ""
	customProxyClients = make(map[string]customProxyClientEntry)
	customProxyClientOperations = 0
	customProxyClientNow = time.Now
	clientLock.Unlock()

	for _, entry := range entries {
		closeCustomProxyClient(entry)
	}
}

func TestGetHTTPClientCustomProxyReusesSameURL(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	first, err := GetHTTPClientCustomProxy("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("首次获取自定义代理客户端失败: %v", err)
	}
	second, err := GetHTTPClientCustomProxy("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("第二次获取自定义代理客户端失败: %v", err)
	}
	if first != second {
		t.Fatal("同一代理地址应复用同一个 http.Client")
	}
}

func TestGetHTTPClientCustomProxyTrimsURL(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	first, err := GetHTTPClientCustomProxy("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("获取自定义代理客户端失败: %v", err)
	}
	second, err := GetHTTPClientCustomProxy("  http://127.0.0.1:8080  ")
	if err != nil {
		t.Fatalf("获取带空白代理地址的客户端失败: %v", err)
	}
	if first != second {
		t.Fatal("代理地址首尾空白不应导致创建不同 http.Client")
	}
}

func TestGetHTTPClientCustomProxySeparatesDifferentURLs(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	first, err := GetHTTPClientCustomProxy("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("获取第一个自定义代理客户端失败: %v", err)
	}
	second, err := GetHTTPClientCustomProxy("http://127.0.0.1:8081")
	if err != nil {
		t.Fatalf("获取第二个自定义代理客户端失败: %v", err)
	}
	if first == second {
		t.Fatal("不同代理地址不应复用同一个 http.Client")
	}
}

func TestGetHTTPClientCustomProxyRejectsEmptyURL(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	if _, err := GetHTTPClientCustomProxy("   "); err == nil {
		t.Fatal("空白代理地址应返回错误")
	}
}

func TestGetHTTPClientCustomProxyExpiresIdleClient(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	customProxyClientNow = func() time.Time { return now }
	proxyURL := "http://127.0.0.1:8080"
	first, err := GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("获取初始自定义代理客户端失败: %v", err)
	}

	now = now.Add(customProxyClientTTL)
	second, err := GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("获取过期后的自定义代理客户端失败: %v", err)
	}
	if first == second {
		t.Fatal("超过 TTL 后不应复用旧的 http.Client")
	}
}

func TestGetHTTPClientCustomProxyRefreshesLastUsed(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	customProxyClientNow = func() time.Time { return now }
	proxyURL := "http://127.0.0.1:8080"
	first, err := GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("获取初始自定义代理客户端失败: %v", err)
	}

	now = now.Add(customProxyClientTTL / 2)
	if _, err := GetHTTPClientCustomProxy(proxyURL); err != nil {
		t.Fatalf("刷新代理客户端最后使用时间失败: %v", err)
	}
	now = now.Add(customProxyClientTTL / 2)
	third, err := GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("刷新 TTL 后获取代理客户端失败: %v", err)
	}
	if first != third {
		t.Fatal("持续使用的代理客户端不应按初次创建时间过期")
	}
}

func TestGetHTTPClientCustomProxySweepsExpiredEntries(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	customProxyClientNow = func() time.Time { return now }
	staleURL := "http://127.0.0.1:8080"
	tracker := &closeTrackingTransport{}
	clientLock.Lock()
	customProxyClients[staleURL] = customProxyClientEntry{
		client:   &http.Client{Transport: tracker},
		lastUsed: now,
	}
	customProxyClientOperations = customProxyClientSweepEvery - 1
	clientLock.Unlock()
	now = now.Add(customProxyClientTTL + time.Second)
	if _, err := GetHTTPClientCustomProxy("http://127.0.0.1:8081"); err != nil {
		t.Fatalf("触发惰性清理失败: %v", err)
	}

	clientLock.RLock()
	_, remains := customProxyClients[staleURL]
	clientLock.RUnlock()
	if remains {
		t.Fatal("惰性清理后仍保留过期代理客户端")
	}
	if got := tracker.closeCalls.Load(); got != 1 {
		t.Fatalf("过期清理的 CloseIdleConnections 调用次数 = %d，期望 1", got)
	}
}

func TestGetHTTPClientCustomProxyEnforcesHardLimit(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	customProxyClientNow = func() time.Time { return now }
	oldestURL := "http://127.0.0.1:10000"
	for i := range customProxyClientMaxEntries + 12 {
		now = now.Add(time.Second)
		proxyURL := fmt.Sprintf("http://127.0.0.1:%d", 10000+i)
		if _, err := GetHTTPClientCustomProxy(proxyURL); err != nil {
			t.Fatalf("创建第 %d 个代理客户端失败: %v", i, err)
		}

		clientLock.RLock()
		size := len(customProxyClients)
		clientLock.RUnlock()
		if size > customProxyClientMaxEntries {
			t.Fatalf("代理客户端缓存大小 = %d，上限 = %d", size, customProxyClientMaxEntries)
		}
	}

	clientLock.RLock()
	_, oldestRemains := customProxyClients[oldestURL]
	clientLock.RUnlock()
	if oldestRemains {
		t.Fatal("达到容量上限后未淘汰最久未使用的代理客户端")
	}
}

func TestInvalidateCustomProxyClientRemovesEntryAndClosesIdleConnections(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	tracker := &closeTrackingTransport{}
	proxyURL := "http://127.0.0.1:8080"
	clientLock.Lock()
	customProxyClients[proxyURL] = customProxyClientEntry{
		client:   &http.Client{Transport: tracker},
		lastUsed: time.Now(),
	}
	clientLock.Unlock()

	InvalidateCustomProxyClient("  " + proxyURL + "  ")

	clientLock.RLock()
	_, remains := customProxyClients[proxyURL]
	clientLock.RUnlock()
	if remains {
		t.Fatal("失效后仍保留代理客户端")
	}
	if got := tracker.closeCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections 调用次数 = %d，期望 1", got)
	}
}

func TestEvictOldestCustomProxyClientClosesIdleConnections(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	oldest := &closeTrackingTransport{}
	newest := &closeTrackingTransport{}
	now := time.Now()
	clientLock.Lock()
	customProxyClients["http://127.0.0.1:8080"] = customProxyClientEntry{
		client: &http.Client{Transport: oldest}, lastUsed: now,
	}
	customProxyClients["http://127.0.0.1:8081"] = customProxyClientEntry{
		client: &http.Client{Transport: newest}, lastUsed: now.Add(time.Second),
	}
	evictOldestCustomProxyClientLocked()
	_, oldestRemains := customProxyClients["http://127.0.0.1:8080"]
	_, newestRemains := customProxyClients["http://127.0.0.1:8081"]
	clientLock.Unlock()

	if oldestRemains || !newestRemains {
		t.Fatalf("LRU 淘汰结果错误：oldest=%t newest=%t", oldestRemains, newestRemains)
	}
	if got := oldest.closeCalls.Load(); got != 1 {
		t.Fatalf("容量淘汰的 CloseIdleConnections 调用次数 = %d，期望 1", got)
	}
	if got := newest.closeCalls.Load(); got != 0 {
		t.Fatalf("未淘汰客户端被关闭 %d 次", got)
	}
}

func TestInvalidateAllCustomProxyClientsClosesEveryEntry(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	first := &closeTrackingTransport{}
	second := &closeTrackingTransport{}
	clientLock.Lock()
	customProxyClients["http://127.0.0.1:8080"] = customProxyClientEntry{
		client: &http.Client{Transport: first}, lastUsed: time.Now(),
	}
	customProxyClients["http://127.0.0.1:8081"] = customProxyClientEntry{
		client: &http.Client{Transport: second}, lastUsed: time.Now(),
	}
	clientLock.Unlock()

	InvalidateAllCustomProxyClients()

	clientLock.RLock()
	size := len(customProxyClients)
	clientLock.RUnlock()
	if size != 0 {
		t.Fatalf("清空后缓存大小 = %d，期望 0", size)
	}
	if first.closeCalls.Load() != 1 || second.closeCalls.Load() != 1 {
		t.Fatalf("清空未关闭所有空闲连接：first=%d second=%d", first.closeCalls.Load(), second.closeCalls.Load())
	}
}

func TestGetHTTPClientCustomProxyConcurrentLifecycle(t *testing.T) {
	resetHTTPClientCacheForTest()
	defer resetHTTPClientCacheForTest()

	errCh := make(chan error, 16*160)
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 160 {
				proxyURL := fmt.Sprintf("http://127.0.0.1:%d", 12000+(worker+i)%192)
				client, err := GetHTTPClientCustomProxy(proxyURL)
				if err != nil {
					errCh <- err
					continue
				}
				if client == nil {
					errCh <- fmt.Errorf("nil client for %s", proxyURL)
				}
				if i%31 == 0 {
					InvalidateCustomProxyClient(proxyURL)
				}
				if worker == 0 && i%79 == 0 {
					InvalidateAllCustomProxyClients()
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("并发缓存操作失败: %v", err)
	}

	clientLock.RLock()
	size := len(customProxyClients)
	clientLock.RUnlock()
	if size > customProxyClientMaxEntries {
		t.Fatalf("并发操作后缓存大小 = %d，上限 = %d", size, customProxyClientMaxEntries)
	}
}

type closeTrackingTransport struct {
	closeCalls atomic.Int32
}

func (t *closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("RoundTrip is not implemented in this test transport")
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closeCalls.Add(1)
}
