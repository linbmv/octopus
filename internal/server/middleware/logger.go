package middleware

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// Logger 输出访问日志（仅 debug 模式挂载）。
// Gemini 兼容路径用 ?key=sk-... 鉴权、日志流用 ?token=... 建连，
// 默认 gin.Logger 会把完整 query 打进日志，必须先脱敏。
func Logger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		return fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s %s\n",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"),
			p.StatusCode,
			p.Latency,
			p.ClientIP,
			p.Method,
			redactSensitiveQuery(p.Path),
		)
	})
}

func redactSensitiveQuery(path string) string {
	u, err := url.Parse(path)
	if err != nil || u.RawQuery == "" {
		return path
	}
	values := u.Query()
	changed := false
	for name := range values {
		switch strings.ToLower(name) {
		case "key", "token":
			values.Set(name, "REDACTED")
			changed = true
		}
	}
	if !changed {
		return path
	}
	u.RawQuery = values.Encode()
	return u.String()
}
