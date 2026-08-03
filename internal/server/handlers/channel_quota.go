package handlers

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

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
			router.NewRoute("/quota", http.MethodGet).
				Handle(getGlobalChannelQuota),
		).
		AddRoute(
			router.NewRoute("/quota/refresh", http.MethodPost).
				Use(middleware.RequireJSON()).
				Handle(refreshGlobalChannelQuota),
		).
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

func globalChannelQuota(c *gin.Context, force bool) {
	channels, err := op.ChannelList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, "list channels for quota failed")
		return
	}
	quotaCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	results := make([][]relay.CodexQuota, len(channels))
	var waitGroup sync.WaitGroup
	for i := range channels {
		if channels[i].Type != model.ChannelTypeOpenAICodex {
			continue
		}
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			results[index] = relay.QueryCodexQuota(quotaCtx, &channels[index], force)
		}(i)
	}
	waitGroup.Wait()
	quotas := make([]relay.CodexQuota, 0)
	for _, result := range results {
		quotas = append(quotas, result...)
	}
	resp.Success(c, quotas)
}

func getGlobalChannelQuota(c *gin.Context) {
	globalChannelQuota(c, false)
}

func refreshGlobalChannelQuota(c *gin.Context) {
	globalChannelQuota(c, true)
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
