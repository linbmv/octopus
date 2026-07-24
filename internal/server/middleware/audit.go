package middleware

import (
	"time"

	projectlog "github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

// AuditEvent 定义审计日志事件类型
type AuditEvent string

const (
	EventUserLogin                    AuditEvent = "user.login"
	EventUserLoginFailed              AuditEvent = "user.login.failed"
	EventUserLogout                   AuditEvent = "user.logout"
	EventPasswordChange               AuditEvent = "user.password.change"
	EventUsernameChange               AuditEvent = "user.username.change"
	EventUsernameChangeFailed         AuditEvent = "user.username.change.failed"
	EventChannelCreate                AuditEvent = "channel.create"
	EventChannelUpdate                AuditEvent = "channel.update"
	EventChannelDelete                AuditEvent = "channel.delete"
	EventChannelCapabilityProbe       AuditEvent = "channel.capability.probe"
	EventChannelSelfHealingPreview    AuditEvent = "channel.self_healing.preview"
	EventChannelSelfHealingCompare    AuditEvent = "channel.self_healing.compare"
	EventChannelSelfHealingDiagnostic AuditEvent = "channel.self_healing.diagnostic"
	EventChannelSelfHealingView       AuditEvent = "channel.self_healing.view"
	EventChannelSelfHealingApply      AuditEvent = "channel.self_healing.apply"
	EventChannelSelfHealingRollback   AuditEvent = "channel.self_healing.rollback"
	EventSettingsUpdate               AuditEvent = "settings.update"
	EventSensitiveOperationDenied     AuditEvent = "sensitive.operation.denied"
)

// AuditLog 记录用户操作审计日志
func AuditLog(c *gin.Context, event AuditEvent, details map[string]interface{}) {
	username, _ := c.Get("username")
	userID, _ := c.Get("user_id")
	requestID, _ := c.Get("request_id")
	traceID, _ := c.Get("trace_id")

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
	if requestID != nil {
		logData["request_id"] = requestID
	}
	if traceID != nil {
		logData["trace_id"] = traceID
	}

	for k, v := range details {
		logData[k] = v
	}

	fields := make([]interface{}, 0, len(logData)*2)
	for key, value := range logData {
		fields = append(fields, key, value)
	}
	projectlog.WithContext(c.Request.Context()).Infow("audit event", fields...)
}
