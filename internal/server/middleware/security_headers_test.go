package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	for header, want := range map[string]string{
		"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := response.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") ||
		!strings.Contains(csp, "object-src 'none'") ||
		!strings.Contains(csp, "script-src-attr 'none'") ||
		!strings.Contains(csp, "connect-src 'self'") ||
		strings.Contains(csp, "connect-src 'self' http:") {
		t.Fatalf("Content-Security-Policy = %q", csp)
	}
}
