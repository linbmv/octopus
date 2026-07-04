package handlers

import (
	"net/http"

	"github.com/bestruirui/octopus/internal/relay"
	relayhealth "github.com/bestruirui/octopus/internal/relay/health"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func init() {
	router.NewGroupRouter("/api/v1/health").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/status", http.MethodGet).
				Handle(getHealthStatus),
		).
		AddRoute(
			router.NewRoute("/status/channel", http.MethodGet).
				Handle(getHealthStatusByChannel),
		).
		AddRoute(
			router.NewRoute("/status/specific", http.MethodGet).
				Handle(getHealthStatusSpecific),
		).
		AddRoute(
			router.NewRoute("/reset", http.MethodPost).
				Handle(resetHealth),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableHealth),
		).
		AddRoute(
			router.NewRoute("/disable", http.MethodPost).
				Handle(disableHealth),
		).
		AddRoute(
			router.NewRoute("/metrics", http.MethodGet).
				Handle(getHealthMetrics),
		)
}

func healthAPI(c *gin.Context) (*relayhealth.HealthAPI, bool) {
	manager := relay.GetHealthManager()
	if manager == nil {
		resp.Error(c, http.StatusServiceUnavailable, "health manager unavailable")
		return nil, false
	}
	return relayhealth.NewHealthAPI(manager), true
}

func getHealthStatus(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleGetAll(c.Writer, c.Request)
}

func getHealthStatusByChannel(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleGetByChannel(c.Writer, c.Request)
}

func getHealthStatusSpecific(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleGetSpecific(c.Writer, c.Request)
}

func resetHealth(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleReset(c.Writer, c.Request)
}

func enableHealth(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleEnable(c.Writer, c.Request)
}

func disableHealth(c *gin.Context) {
	api, ok := healthAPI(c)
	if !ok {
		return
	}
	api.HandleDisable(c.Writer, c.Request)
}

func getHealthMetrics(c *gin.Context) {
	if relay.GetHealthMetrics() == nil || relay.GetHealthManager() == nil {
		resp.Error(c, http.StatusServiceUnavailable, "health metrics unavailable")
		return
	}
	relay.RefreshHealthMetrics()
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}
