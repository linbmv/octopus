package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
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

	body := &oversizedReader{remaining: conf.Default().Relay.MaxJSONRequestBytes + 1}
	req := httptest.NewRequest(http.MethodPost, "/json", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRequireJSONSkipsBodylessMethods(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			router := gin.New()
			router.Handle(method, "/json", RequireJSON(), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "/json", nil))
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
		})
	}
}

func TestRequireJSONRejectsUnsupportedContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/json", RequireJSON(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/json", strings.NewReader("payload"))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
	}
}

func TestRequireJSONRejectsCompressedBodyBeforeDecompression(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatalf("compress body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	called := false
	router := gin.New()
	router.POST("/json", RequireJSON(), func(c *gin.Context) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/json", &compressed)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusUnsupportedMediaType || called {
		t.Fatalf("compressed request = status %d called=%t", response.Code, called)
	}
	assertResponseErrorCode(t, response.Body.Bytes(), "REQUEST_CONTENT_ENCODING_UNSUPPORTED")
}

func TestRequireImageMultipartLimitsAndValidatesWireFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		contentType string
		encoding    string
		length      int64
		wantStatus  int
		wantCode    string
	}{
		{name: "valid", contentType: "multipart/form-data; boundary=test", length: 1, wantStatus: http.StatusNoContent},
		{name: "wrong type", contentType: "application/json", length: 1, wantStatus: http.StatusUnsupportedMediaType, wantCode: "REQUEST_CONTENT_TYPE_UNSUPPORTED"},
		{name: "compressed", contentType: "multipart/form-data; boundary=test", encoding: "zstd", length: 1, wantStatus: http.StatusUnsupportedMediaType, wantCode: "REQUEST_CONTENT_ENCODING_UNSUPPORTED"},
		{name: "declared too large", contentType: "multipart/form-data; boundary=test", length: conf.Default().Relay.MaxImageRequestBytes + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "REQUEST_BODY_TOO_LARGE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/image", RequireImageMultipart(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodPost, "/image", strings.NewReader("x"))
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("Content-Encoding", test.encoding)
			req.ContentLength = test.length
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode != "" {
				assertResponseErrorCode(t, response.Body.Bytes(), test.wantCode)
			}
		})
	}
}

func assertResponseErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, body)
	}
	if response.Error.Code != want {
		t.Fatalf("error code = %q, want %q", response.Error.Code, want)
	}
}
