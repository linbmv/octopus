package middleware

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// AuditEvent 定义审计日志事件类型
type AuditEvent string

const (
	EventUserLogin          AuditEvent = "user.login"
	EventUserLoginFailed    AuditEvent = "user.login.failed"
	EventUserLogout         AuditEvent = "user.logout"
	EventPasswordChange     AuditEvent = "user.password.change"
	EventUsernameChange     AuditEvent = "user.username.change"
	EventChannelCreate      AuditEvent = "channel.create"
	EventChannelUpdate      AuditEvent = "channel.update"
	EventChannelDelete      AuditEvent = "channel.delete"
	EventSettingsUpdate     AuditEvent = "settings.update"
	EventTokenRevoke        AuditEvent = "token.revoke"
)

// AuditLog 记录用户操作审计日志
func AuditLog(c *gin.Context, event AuditEvent, details map[string]interface{}) {
	username, _ := c.Get("username")
	userID, _ := c.Get("user_id")

	logData := map[string]interface{}{
		"type":       "audit",
		"event":      string(event),
		"method":     c.Request.Method,
		"path":       c.Request.URL.Path,
		"client_ip":  c.ClientIP(),
		"user_agent": c.Request.UserAgent(),
		"timestamp":  time.Now().Format(time.RFC3339),
	}

	if username != nil {
		logData["username"] = username
	}
	if userID != nil {
		logData["user_id"] = userID
	}

	for k, v := range details {
		logData[k] = v
	}

	jsonData, _ := json.Marshal(logData)
	log.Println(string(jsonData))
}
