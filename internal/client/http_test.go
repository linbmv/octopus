package client

import (
	"net/http"
	"testing"
)

func resetHTTPClientCacheForTest() {
	clientLock.Lock()
	defer clientLock.Unlock()
	systemDirectClient = nil
	systemProxyClient = nil
	systemProxyURL = ""
	customProxyClients = make(map[string]*http.Client)
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
