package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

const maxRequestIDLength = 128

var (
	requestIDPattern         = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	requestIDFallbackCounter atomic.Uint64
)

// RequestID 中间件为每个请求生成唯一 ID，用于追踪和关联日志
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if !validRequestID(requestID) {
			requestID = generateRequestID()
		}

		// 设置到上下文和响应头
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Request = c.Request.WithContext(log.WithRequestID(c.Request.Context(), requestID))

		c.Next()
	}
}

func validRequestID(value string) bool {
	return value != "" && len(value) <= maxRequestIDLength && requestIDPattern.MatchString(value)
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Keep fallback IDs unique enough for tracing even if the OS random source
		// is temporarily unavailable. This is not used as a security token.
		return fmt.Sprintf("%016x%016x", uint64(time.Now().UnixNano()), requestIDFallbackCounter.Add(1))
	}
	return hex.EncodeToString(b)
}
