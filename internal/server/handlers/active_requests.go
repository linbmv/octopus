package handlers

import (
	"net/http"

	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/active-requests").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("", http.MethodGet).
				Handle(GetActiveRequests),
		).
		AddRoute(
			router.NewRoute("/count", http.MethodGet).
				Handle(GetActiveRequestsCount),
		)
}

// GetActiveRequests 获取所有活跃请求
func GetActiveRequests(c *gin.Context) {
	requests := relay.GetActiveRequests()
	c.JSON(http.StatusOK, gin.H{
		"total":    len(requests),
		"requests": requests,
	})
}

// GetActiveRequestsCount 获取活跃请求数量
func GetActiveRequestsCount(c *gin.Context) {
	count := relay.GetActiveRequestCount()
	c.JSON(http.StatusOK, gin.H{
		"count": count,
	})
}
