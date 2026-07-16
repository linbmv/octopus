package handlers

import (
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/stats").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/today", http.MethodGet).
				Handle(getStatsToday),
		).
		AddRoute(
			router.NewRoute("/daily", http.MethodGet).
				Handle(getStatsDaily),
		).
		AddRoute(
			router.NewRoute("/hourly", http.MethodGet).
				Handle(getStatsHourly),
		).
		AddRoute(
			router.NewRoute("/total", http.MethodGet).
				Handle(getStatsTotal),
		).
		AddRoute(
			router.NewRoute("/apikey", http.MethodGet).
				Handle(getStatsAPIKey),
		).
		AddRoute(
			router.NewRoute("/error-levels", http.MethodGet).
				Handle(getStatsErrorLevels),
		)
}

func getStatsToday(c *gin.Context) {
	resp.Success(c, op.StatsTodayGet())
}

func getStatsDaily(c *gin.Context) {
	statsDaily, err := op.StatsGetDaily(c.Request.Context())
	if err != nil {
		respondInternalError(c, "get daily statistics failed", err)
		return
	}
	resp.Success(c, statsDaily)
}

func getStatsHourly(c *gin.Context) {
	resp.Success(c, op.StatsHourlyGet())
}

func getStatsTotal(c *gin.Context) {
	resp.Success(c, op.StatsTotalGet())
}

func getStatsAPIKey(c *gin.Context) {
	resp.Success(c, op.StatsAPIKeyList())
}

func getStatsErrorLevels(c *gin.Context) {
	windowHours, err := strconv.Atoi(c.DefaultQuery("window_hours", strconv.Itoa(op.StatsErrorLevelsDefaultWindowHours)))
	if err != nil || windowHours < 1 || windowHours > op.StatsErrorLevelsMaxWindowHours {
		resp.Error(c, http.StatusBadRequest, "window_hours must be an integer between 1 and "+strconv.Itoa(op.StatsErrorLevelsMaxWindowHours))
		return
	}
	channelID, err := strconv.Atoi(c.DefaultQuery("channel_id", "0"))
	if err != nil || channelID < 0 {
		resp.Error(c, http.StatusBadRequest, "channel_id must be a non-negative integer")
		return
	}

	stats, err := op.StatsErrorLevelsGet(c.Request.Context(), windowHours, channelID)
	if err != nil {
		respondInternalError(c, "get error-level statistics failed", err)
		return
	}
	resp.Success(c, stats)
}
