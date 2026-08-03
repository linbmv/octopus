package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	projectlog "github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestAuditLogIncludesContextAndDetails(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	oldLogger := projectlog.Logger
	projectlog.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { projectlog.Logger = oldLogger })

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/api/resource", nil)
	context.Request.Header.Set("User-Agent", "audit-test")
	context.Set("username", "admin")
	context.Set("user_id", 7)
	context.Set("request_id", "request-1")

	AuditLog(context, EventSettingsUpdate, map[string]interface{}{"setting": "theme"})
	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	for key, want := range map[string]interface{}{
		"event":      "settings.update",
		"username":   "admin",
		"user_id":    int64(7),
		"request_id": "request-1",
		"setting":    "theme",
	} {
		if got := fields[key]; got != want {
			t.Errorf("audit field %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestStaticEmbedServesFilesAndSkipsAPIPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	files := fstest.MapFS{
		"asset.txt":                         {Data: []byte("asset body")},
		"index.html":                        {Data: []byte("app shell")},
		"locale/zh_hans.json":               {Data: []byte("locale catalog")},
		"sw.js":                             {Data: []byte("service worker")},
		"_next/static/chunks/app-abc123.js": {Data: []byte("hashed chunk")},
	}
	router := gin.New()
	router.Use(StaticEmbed("", files))
	router.GET("/api/ping", func(c *gin.Context) { c.String(http.StatusOK, "api") })

	tests := []struct {
		path         string
		body         string
		cacheControl string
	}{
		{path: "/", body: "app shell", cacheControl: revalidateCacheControl},
		{path: "/asset.txt", body: "asset body", cacheControl: revalidateCacheControl},
		{path: "/locale/zh_hans.json", body: "locale catalog", cacheControl: revalidateCacheControl},
		{path: "/sw.js", body: "service worker", cacheControl: revalidateCacheControl},
		{path: "/_next/static/chunks/app-abc123.js", body: "hashed chunk", cacheControl: immutableStaticCacheControl},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || response.Body.String() != test.body {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != test.cacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cacheControl)
			}
		})
	}

	api := httptest.NewRecorder()
	router.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/ping", nil))
	if api.Code != http.StatusOK || api.Body.String() != "api" {
		t.Fatalf("API response = %d %q", api.Code, api.Body.String())
	}
}

func TestLoggerMiddlewareHandlesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Logger())
	router.GET("/logged", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/logged?token=secret", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
