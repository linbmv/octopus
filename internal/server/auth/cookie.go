package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CookieName 是存放会话 token 的 Cookie 名。
const CookieName = "auth"

// SetAuthCookie 写入会话 Cookie。始终带 HttpOnly，使 token 无法被页面脚本读取；
// Secure 按请求实际是否经 TLS 判定，以免 HTTP 内网部署直接登录不上。
func SetAuthCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isRequestSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearAuthCookie 使会话 Cookie 立即过期，属性需与写入时一致，否则浏览器不会覆盖。
func ClearAuthCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isRequestSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// isRequestSecure 判定请求是否经由 TLS，兼容反向代理转发头。
func isRequestSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	proto := c.GetHeader("X-Forwarded-Proto")
	if proto == "" {
		return false
	}
	// 多级代理会拼成 "https, http"，取第一跳即客户端侧协议。
	if idx := strings.IndexByte(proto, ','); idx >= 0 {
		proto = proto[:idx]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
