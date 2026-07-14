package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func noopHandler(c *gin.Context) {}

// TestRegisterRoute 验证 P0-3 / spec [路由] 规范：
// 路由声明错误（空 handler、未知 method）必须返回 error，正常方法正常注册。
func TestRegisterRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		method   string
		handlers []gin.HandlerFunc
		wantErr  bool
	}{
		{"valid GET", http.MethodGet, []gin.HandlerFunc{noopHandler}, false},
		{"valid POST", http.MethodPost, []gin.HandlerFunc{noopHandler}, false},
		{"valid DELETE", http.MethodDelete, []gin.HandlerFunc{noopHandler}, false},
		{"empty handlers", http.MethodGet, nil, true},
		{"unknown method", "TRACE", []gin.HandlerFunc{noopHandler}, true},
		{"lowercase method not normalized", "get", []gin.HandlerFunc{noopHandler}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := gin.New()
			group := engine.Group("/test")
			err := registerRoute(group, tt.method, "/path", tt.handlers)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %s: %v", tt.name, err)
			}
		})
	}
}

// TestRegisterAllPropagatesError 验证未知 method 的路由会让 RegisterAll 整体失败，
// 而不是静默降级为 GET。
func TestRegisterAllPropagatesError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 保存并隔离全局注册表，避免污染其他测试。
	saved := registeredRouters
	registeredRouters = nil
	defer func() { registeredRouters = saved }()

	g := NewGroupRouter("/api")
	bad := NewRoute("/x", "BADMETHOD")
	bad.Handle(noopHandler)
	g.AddRoute(bad)

	engine := gin.New()
	if err := RegisterAll(engine); err == nil {
		t.Fatal("expected RegisterAll to fail on unknown method, got nil")
	}
}

// TestRegisterAllSuccess 验证正常路由组可成功注册。
func TestRegisterAllSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := registeredRouters
	registeredRouters = nil
	defer func() { registeredRouters = saved }()

	g := NewGroupRouter("/api")
	r := NewRoute("/ok", http.MethodPost)
	r.Handle(noopHandler)
	g.AddRoute(r)

	engine := gin.New()
	if err := RegisterAll(engine); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
