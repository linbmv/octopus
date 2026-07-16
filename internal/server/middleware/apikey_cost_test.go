package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	serverauth "github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

func TestAPIKeyAuthReturnsStableQuotaErrors(t *testing.T) {
	key := setupAPIKeyCostMiddlewareTest(t, 1)
	if err := op.StatsAPIKeyUpdate(key.ID, model.StatsMetrics{InputCost: 1}); err != nil {
		t.Fatalf("StatsAPIKeyUpdate() error = %v", err)
	}

	router := gin.New()
	router.GET("/limited", APIKeyAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response := performAPIKeyRequest(router, http.MethodGet, "/limited", key.APIKey, nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	var body resp.ResponseStruct
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Message != op.ErrAPIKeyMaxCostReached.Error() || body.Error == nil || body.Error.Code != "RATE_LIMITED" {
		t.Fatalf("quota response = %#v, want stable RATE_LIMITED/%q", body, op.ErrAPIKeyMaxCostReached.Error())
	}
}

func TestAPIKeyAuthResponsesAreNotCacheable(t *testing.T) {
	key := setupAPIKeyCostMiddlewareTest(t, 0)
	router := gin.New()
	router.GET("/models", APIKeyAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	response := performAPIKeyRequest(router, http.MethodGet, "/models", key.APIKey, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAPIKeyAuthReservationReleasedAfterHandlerCompletion(t *testing.T) {
	key := setupAPIKeyCostMiddlewareTest(t, 10)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	router := gin.New()
	router.POST("/limited", APIKeyAuth(), func(c *gin.Context) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		c.Status(http.StatusNoContent)
	})
	router.GET("/models", APIKeyAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil) }()
	<-entered

	blocked := performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent status = %d, want %d; body=%s", blocked.Code, http.StatusTooManyRequests, blocked.Body.String())
	}
	var body resp.ResponseStruct
	if err := json.Unmarshal(blocked.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode concurrent response: %v", err)
	}
	if body.Message != op.ErrAPIKeyMaxCostReserved.Error() {
		t.Fatalf("concurrent message = %q, want %q", body.Message, op.ErrAPIKeyMaxCostReserved.Error())
	}
	if retryAfter := blocked.Header().Get("Retry-After"); retryAfter != "1" {
		t.Fatalf("Retry-After = %q, want 1", retryAfter)
	}
	if readOnly := performAPIKeyRequest(router, http.MethodGet, "/models", key.APIKey, nil); readOnly.Code != http.StatusNoContent {
		t.Fatalf("read-only status during generation = %d, want %d; body=%s", readOnly.Code, http.StatusNoContent, readOnly.Body.String())
	}

	close(release)
	if first := <-firstDone; first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusNoContent)
	}
	if after := performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil); after.Code != http.StatusNoContent {
		t.Fatalf("status after completion = %d, want %d; body=%s", after.Code, http.StatusNoContent, after.Body.String())
	}
}

func TestAPIKeyAuthReservationReleasedAfterCancellation(t *testing.T) {
	key := setupAPIKeyCostMiddlewareTest(t, 10)
	entered := make(chan struct{})
	var calls atomic.Int32
	router := gin.New()
	router.POST("/limited", APIKeyAuth(), func(c *gin.Context) {
		if calls.Add(1) == 1 {
			close(entered)
			<-c.Request.Context().Done()
		}
		c.Status(http.StatusNoContent)
	})

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, ctx) }()
	<-entered
	cancel()
	<-firstDone

	if after := performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil); after.Code != http.StatusNoContent {
		t.Fatalf("status after cancellation = %d, want %d; body=%s", after.Code, http.StatusNoContent, after.Body.String())
	}
}

func TestAPIKeyAuthReservationReleasedDuringPanicUnwind(t *testing.T) {
	key := setupAPIKeyCostMiddlewareTest(t, 10)
	var calls atomic.Int32
	router := gin.New()
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ interface{}) {
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	router.POST("/limited", APIKeyAuth(), func(c *gin.Context) {
		if calls.Add(1) == 1 {
			panic("test panic")
		}
		c.Status(http.StatusNoContent)
	})

	if first := performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil); first.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", first.Code, http.StatusInternalServerError)
	}
	if after := performAPIKeyRequest(router, http.MethodPost, "/limited", key.APIKey, nil); after.Code != http.StatusNoContent {
		t.Fatalf("status after panic = %d, want %d; body=%s", after.Code, http.StatusNoContent, after.Body.String())
	}
}

func setupAPIKeyCostMiddlewareTest(t *testing.T, maxCost float64) model.APIKey {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "apikey-cost.db"), false); err != nil {
		t.Fatalf("init db: %v", err)
	}
	secret, err := serverauth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate API key: %v", err)
	}
	key := model.APIKey{
		Name: "middleware-cost-test", APIKey: secret, Enabled: true, MaxCost: maxCost,
	}
	if err := op.APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() error = %v", err)
	}
	t.Cleanup(func() {
		_ = op.APIKeyDelete(key.ID, context.Background())
		_ = db.Close()
	})
	return key
}

func performAPIKeyRequest(handler http.Handler, method, path, secret string, ctx context.Context) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
