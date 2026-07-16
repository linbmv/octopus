package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimiterEnforcesLimitAndResetsAfterWindow(t *testing.T) {
	limiter := NewRateLimiter(2, 20*time.Millisecond)
	if !limiter.Allow("client") {
		t.Fatal("first request within the limit should be allowed")
	}
	if !limiter.Allow("client") {
		t.Fatal("second request within the limit should be allowed")
	}
	if limiter.Allow("client") {
		t.Fatal("request above the limit should be rejected")
	}
	time.Sleep(30 * time.Millisecond)
	if !limiter.Allow("client") {
		t.Fatal("request should be allowed after the window expires")
	}
}

func TestRateLimitMiddlewareReturnsTooManyRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/limited", RateLimit(1, time.Minute), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/limited", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusNoContent)
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/limited", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimiterCleansStaleBucketsWithoutBackgroundWorker(t *testing.T) {
	limiter := NewRateLimiter(2, time.Minute)
	now := time.Now()
	limiter.buckets["stale"] = &bucket{lastSeen: now.Add(-limiter.cleanTTL - time.Second)}
	limiter.lastClean = now.Add(-limiter.window - time.Second)

	if !limiter.Allow("active") {
		t.Fatal("active request should be allowed")
	}
	if _, exists := limiter.buckets["stale"]; exists {
		t.Fatal("stale bucket was not removed opportunistically")
	}
}
