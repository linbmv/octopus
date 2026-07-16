package middleware

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/gin-gonic/gin"
)

func RequireJSON() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet ||
			c.Request.Method == http.MethodDelete ||
			c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
			resp.Error(c, http.StatusUnsupportedMediaType, resp.ErrInvalidJSON)
			c.Abort()
			return
		}
		if !prepareBoundedRequestBody(c, conf.Current().Relay.MaxJSONRequestBytes) {
			return
		}

		c.Next()
	}
}

// RequireImageMultipart enforces the only wire format accepted by the OpenAI
// image edit/variation transformers and applies a request-wide byte ceiling
// before any multipart part is parsed or copied.
func RequireImageMultipart() gin.HandlerFunc {
	return func(c *gin.Context) {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
			resp.ErrorWithCode(c, http.StatusUnsupportedMediaType, "REQUEST_CONTENT_TYPE_UNSUPPORTED", "image edits and variations require multipart/form-data")
			c.Abort()
			return
		}
		if !prepareBoundedRequestBody(c, conf.Current().Relay.MaxImageRequestBytes) {
			return
		}
		c.Next()
	}
}

func prepareBoundedRequestBody(c *gin.Context, maxBytes int64) bool {
	encoding := strings.TrimSpace(c.GetHeader("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		resp.ErrorWithCode(c, http.StatusUnsupportedMediaType, "REQUEST_CONTENT_ENCODING_UNSUPPORTED", "compressed request bodies are not supported")
		c.Abort()
		return false
	}
	if maxBytes <= 0 || c.Request.ContentLength > maxBytes {
		resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
		c.Abort()
		return false
	}
	body, err := bodylimit.ReadAll(c.Request.Body, maxBytes)
	if err != nil {
		if errors.Is(err, bodylimit.ErrTooLarge) {
			resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
		} else {
			resp.ErrorWithCode(c, http.StatusBadRequest, "REQUEST_BODY_READ_FAILED", "failed to read request body")
		}
		c.Abort()
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request = c.Request.WithContext(bodylimit.WithBufferedBody(c.Request.Context(), body))
	return true
}
