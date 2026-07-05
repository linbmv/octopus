package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/gin-gonic/gin"
)

type importOversizedReader struct {
	remaining int64
}

func (r *importOversizedReader) Read(p []byte) (int, error) {
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

func TestImportDBRejectsOversizedRawBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/import", importDB)

	req := httptest.NewRequest(http.MethodPost, "/import", &importOversizedReader{remaining: conf.MaxDBImportBytes + 1})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
}
