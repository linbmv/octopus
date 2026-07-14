package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// RequestID 中间件为每个请求生成唯一 ID，用于追踪和关联日志
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 优先使用客户端提供的 Request ID
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			// 生成新的 Request ID
			requestID = generateRequestID()
		}

		// 设置到上下文和响应头
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)

		c.Next()
	}
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 降级：使用时间戳
		return hex.EncodeToString([]byte(string(rune(b[0]))))
	}
	return hex.EncodeToString(b)
}
