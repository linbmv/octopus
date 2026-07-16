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
	files := fstest.MapFS{"asset.txt": {Data: []byte("asset body")}}
	router := gin.New()
	router.Use(StaticEmbed("", files))
	router.GET("/api/ping", func(c *gin.Context) { c.String(http.StatusOK, "api") })

	asset := httptest.NewRecorder()
	router.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/asset.txt", nil))
	if asset.Code != http.StatusOK || asset.Body.String() != "asset body" {
		t.Fatalf("asset response = %d %q", asset.Code, asset.Body.String())
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
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
