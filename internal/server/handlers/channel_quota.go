package handlers

import (
	"net/http"
	"strconv"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/:id/quota", http.MethodGet).
				Handle(getChannelQuota),
		).
		AddRoute(
			router.NewRoute("/:id/quota/refresh", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(refreshChannelQuota),
		)
}

func channelQuota(c *gin.Context, force bool) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	channel, err := op.ChannelGet(channelID, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "channel not found")
		return
	}
	if channel.Type != model.ChannelTypeOpenAICodex {
		resp.ErrorWithCode(c, http.StatusConflict, "CODEX_QUOTA_UNSUPPORTED", "channel is not a Codex OAuth channel")
		return
	}
	resp.Success(c, relay.QueryCodexQuota(c.Request.Context(), channel, force))
}

func getChannelQuota(c *gin.Context) {
	channelQuota(c, false)
}

func refreshChannelQuota(c *gin.Context) {
	channelQuota(c, true)
}
