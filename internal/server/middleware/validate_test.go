package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/gin-gonic/gin"
)

type oversizedReader struct {
	remaining int64
}

func (r *oversizedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	for i := range p {
		p[i] = ' '
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func TestRequireJSONLimitsRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/json", RequireJSON(), func(c *gin.Context) {
		_, err := io.Copy(io.Discard, c.Request.Body)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "http: request body too large") {
				status = http.StatusRequestEntityTooLarge
			}
			c.Status(status)
			return
		}
		c.Status(http.StatusOK)
	})

	body := &oversizedReader{remaining: conf.MaxJSONRequestBodyBytes + 1}
	req := httptest.NewRequest(http.MethodPost, "/json", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}
