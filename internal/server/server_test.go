package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGeminiContentActionOnlyAllowsContentActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []string{
		"/v1beta/models/gemini-1.5-flash:generateContent",
		"/v1beta/models/gemini-1.5-flash:streamGenerateContent",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			called := false
			r.POST("/v1beta/models/*action", geminiContentActionOnly(func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
			}
			if !called {
				t.Fatal("next handler was not called")
			}
		})
	}
}

func TestGeminiContentActionOnlyRejectsUnsupportedActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []string{
		"/v1beta/models/gemini-1.5-flash:countTokens",
		"/v1beta/models/gemini-1.5-flash:batchGenerateContent",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			called := false
			r.POST("/v1beta/models/*action", geminiContentActionOnly(func(c *gin.Context) {
				called = true
				c.Status(http.StatusNoContent)
			}))

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
			if called {
				t.Fatal("next handler was called")
			}
		})
	}
}
