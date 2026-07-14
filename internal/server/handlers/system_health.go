package handlers

import (
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

var startTime = time.Now()

func init() {
	router.NewGroupRouter("/").
		AddRoute(
			router.NewRoute("/health", http.MethodGet).
				Handle(healthCheck),
		).
		AddRoute(
			router.NewRoute("/readiness", http.MethodGet).
				Handle(readinessCheck),
		).
		AddRoute(
			router.NewRoute("/liveness", http.MethodGet).
				Handle(livenessCheck),
		)
}

type HealthStatus struct {
	Status    string                 `json:"status"`
	Timestamp string                 `json:"timestamp"`
	Uptime    string                 `json:"uptime"`
	Version   string                 `json:"version"`
	Checks    map[string]CheckResult `json:"checks,omitempty"`
}

type CheckResult struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func healthCheck(c *gin.Context) {
	checks := make(map[string]CheckResult)

	dbStatus := checkDatabase()
	checks["database"] = dbStatus

	overallStatus := "healthy"
	if dbStatus.Status != "healthy" {
		overallStatus = "degraded"
	}

	health := HealthStatus{
		Status:    overallStatus,
		Timestamp: time.Now().Format(time.RFC3339),
		Uptime:    time.Since(startTime).String(),
		Version:   conf.APP_NAME,
		Checks:    checks,
	}

	statusCode := http.StatusOK
	if overallStatus == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, health)
}

func readinessCheck(c *gin.Context) {
	dbStatus := checkDatabase()

	if dbStatus.Status == "healthy" {
		resp.Success(c, gin.H{
			"status": "ready",
		})
	} else {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":  "not_ready",
			"message": dbStatus.Message,
		})
	}
}

func livenessCheck(c *gin.Context) {
	resp.Success(c, gin.H{
		"status": "alive",
	})
}

func checkDatabase() CheckResult {
	gormDB := db.GetDB()
	if gormDB == nil {
		return CheckResult{
			Status:  "unhealthy",
			Message: "database not initialized",
		}
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return CheckResult{
			Status:  "unhealthy",
			Message: "failed to get database instance",
		}
	}

	if err := sqlDB.Ping(); err != nil {
		return CheckResult{
			Status:  "unhealthy",
			Message: err.Error(),
		}
	}

	return CheckResult{
		Status: "healthy",
	}
}
