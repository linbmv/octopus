package middleware

import (
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

// ConfirmationToken 存储待确认的敏感操作信息
type ConfirmationToken struct {
	Username  string
	Operation string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// RequireConfirmation 中间件用于标记需要二次确认的敏感操作
// 敏感操作包括：修改密码、修改用户名、删除账户等
// 客户端需在请求体中提供 current_password 字段作为确认凭证
func RequireConfirmation() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 标记当前请求为敏感操作，需要二次确认
		c.Set("requires_confirmation", true)
		c.Next()
	}
}

// ValidateConfirmation 验证敏感操作的确认凭证
// 要求请求体中包含 current_password 字段
func ValidateConfirmation(c *gin.Context) bool {
	requiresConfirmation, exists := c.Get("requires_confirmation")
	if !exists || !requiresConfirmation.(bool) {
		return true // 不需要确认的操作直接通过
	}

	// 检查请求体中是否包含确认字段
	var req struct {
		CurrentPassword string `json:"current_password"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return false
	}

	if req.CurrentPassword == "" {
		AuditLog(c, EventSensitiveOperationDenied, map[string]interface{}{
			"reason": "missing_confirmation",
			"path":   c.Request.URL.Path,
		})
		resp.Error(c, http.StatusBadRequest, "current_password is required for this operation")
		return false
	}

	return true
}
