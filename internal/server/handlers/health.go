package handlers

import (
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/gin-gonic/gin"
)

// HealthCheck 返回服务基本健康状态（不检查依赖）
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// ReadinessCheck 返回服务就绪状态（检查数据库连接）
func ReadinessCheck(c *gin.Context) {
	sqlDB, err := db.GetDB().DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"reason": "database connection unavailable",
			"time":   time.Now().Format(time.RFC3339),
		})
		return
	}

	if err := sqlDB.Ping(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"reason": "database ping failed",
			"time":   time.Now().Format(time.RFC3339),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ready",
		"time":   time.Now().Format(time.RFC3339),
	})
}
