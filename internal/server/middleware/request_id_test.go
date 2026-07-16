package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDUsesProvidedValue(t *testing.T) {
	response, contextValue := serveRequestID(t, "client-request-id")
	if got := response.Header().Get("X-Request-ID"); got != "client-request-id" {
		t.Fatalf("response request ID = %q", got)
	}
	if contextValue != "client-request-id" {
		t.Fatalf("context request ID = %#v", contextValue)
	}
}

func TestRequestIDGeneratesHexValue(t *testing.T) {
	response, contextValue := serveRequestID(t, "")
	requestID := response.Header().Get("X-Request-ID")
	if matched := regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(requestID); !matched {
		t.Fatalf("generated request ID = %q, want 32 lowercase hex characters", requestID)
	}
	if contextValue != requestID {
		t.Fatalf("context request ID = %#v, want %q", contextValue, requestID)
	}
}

func TestRequestIDRejectsUntrustedValues(t *testing.T) {
	tests := map[string]string{
		"too long":   strings.Repeat("a", maxRequestIDLength+1),
		"whitespace": "request id",
		"control":    "request\nvalue",
		"non ascii":  "请求",
	}
	for name, provided := range tests {
		t.Run(name, func(t *testing.T) {
			response, contextValue := serveRequestID(t, provided)
			requestID := response.Header().Get("X-Request-ID")
			if requestID == provided || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(requestID) {
				t.Fatalf("request ID = %q, want a generated 32-character hex value", requestID)
			}
			if contextValue != requestID {
				t.Fatalf("context request ID = %#v, want %q", contextValue, requestID)
			}
		})
	}
}

func serveRequestID(t *testing.T, provided string) (*httptest.ResponseRecorder, interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var contextValue interface{}
	router.GET("/", RequestID(), func(c *gin.Context) {
		contextValue, _ = c.Get("request_id")
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if provided != "" {
		req.Header.Set("X-Request-ID", provided)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response, contextValue
}
