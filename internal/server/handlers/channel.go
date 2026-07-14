package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/dlclark/regexp2"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listChannel),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateChannel),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableChannel),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannel),
		).
		AddRoute(
			router.NewRoute("/fetch-model", http.MethodPost).
				Handle(fetchModel),
		)
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/sync", http.MethodPost).
				Handle(syncChannel),
		).
		AddRoute(
			router.NewRoute("/last-sync-time", http.MethodGet).
				Handle(getLastSyncTime),
		)
}

func listChannel(c *gin.Context) {
	channels, err := op.ChannelList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	for i, channel := range channels {
		stats := op.StatsChannelGet(channel.ID)
		channels[i].Stats = &stats
	}
	resp.Success(c, channels)
}

func createChannel(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelCreate(&channel, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.AuditLog(c, middleware.EventChannelCreate, map[string]interface{}{
		"channel_id":   channel.ID,
		"channel_name": channel.Name,
		"type":         channel.Type,
	})
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	task.SubmitChannelMaintenance(channel)
	resp.Success(c, channel)
}

func updateChannel(c *gin.Context) {
	var req model.ChannelUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.AuditLog(c, middleware.EventChannelUpdate, map[string]interface{}{
		"channel_id":   channel.ID,
		"channel_name": channel.Name,
	})
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	task.SubmitChannelMaintenance(*channel)
	resp.Success(c, channel)
}

func enableChannel(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.ChannelDel(idNum, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.AuditLog(c, middleware.EventChannelDelete, map[string]interface{}{
		"channel_id": idNum,
	})
	resp.Success(c, nil)
}

type fetchModelRequest struct {
	Type          llm.APIFormat        `json:"type"`
	BaseUrls      []model.BaseUrl      `json:"base_urls"`
	Keys          []fetchModelKey      `json:"keys"`
	Proxy         bool                 `json:"proxy"`
	ChannelProxy  *string              `json:"channel_proxy"`
	MatchRegex    *string              `json:"match_regex"`
	CustomHeaders []model.CustomHeader `json:"custom_header"`
}

type fetchModelKey struct {
	Enabled    *bool  `json:"enabled"`
	ChannelKey string `json:"channel_key"`
}

func (r fetchModelRequest) toChannel() (model.Channel, error) {
	if r.Type == "" {
		return model.Channel{}, errors.New("channel type is required")
	}

	baseUrls := make([]model.BaseUrl, 0, len(r.BaseUrls))
	for _, baseURL := range r.BaseUrls {
		rawURL := strings.TrimSpace(baseURL.URL)
		if rawURL == "" {
			continue
		}
		if baseURL.Delay < 0 {
			return model.Channel{}, errors.New("base_urls delay must be greater than or equal to 0")
		}
		if !isValidFetchBaseURL(rawURL) {
			return model.Channel{}, errors.New("base_urls contains invalid URL")
		}
		baseUrls = append(baseUrls, model.BaseUrl{
			URL:   rawURL,
			Delay: baseURL.Delay,
		})
	}
	if len(baseUrls) == 0 {
		return model.Channel{}, errors.New("base_urls is required")
	}

	keys := make([]model.ChannelKey, 0, len(r.Keys))
	hasEnabledKey := false
	for _, key := range r.Keys {
		channelKey := strings.TrimSpace(key.ChannelKey)
		if channelKey == "" {
			continue
		}
		enabled := true
		if key.Enabled != nil {
			enabled = *key.Enabled
		}
		if enabled {
			hasEnabledKey = true
		}
		keys = append(keys, model.ChannelKey{
			Enabled:    enabled,
			ChannelKey: channelKey,
		})
	}
	if len(keys) == 0 {
		return model.Channel{}, errors.New("keys is required")
	}
	if !hasEnabledKey {
		return model.Channel{}, errors.New("at least one enabled API key is required")
	}

	matchRegex := trimOptionalString(r.MatchRegex)
	if matchRegex != nil {
		if _, err := regexp2.Compile(*matchRegex, regexp2.ECMAScript); err != nil {
			return model.Channel{}, errors.New("match_regex is invalid")
		}
	}

	return model.Channel{
		Name:         "fetch-model",
		Type:         r.Type,
		BaseUrls:     baseUrls,
		Keys:         keys,
		Proxy:        r.Proxy,
		ChannelProxy: trimOptionalString(r.ChannelProxy),
		MatchRegex:   matchRegex,
		CustomHeader: trimCustomHeaders(r.CustomHeaders),
	}, nil
}

func isValidFetchBaseURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func trimOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func trimCustomHeaders(headers []model.CustomHeader) []model.CustomHeader {
	result := make([]model.CustomHeader, 0, len(headers))
	for _, header := range headers {
		key := strings.TrimSpace(header.HeaderKey)
		if key == "" {
			continue
		}
		result = append(result, model.CustomHeader{
			HeaderKey:   key,
			HeaderValue: header.HeaderValue,
		})
	}
	return result
}

func fetchModel(c *gin.Context) {
	var request fetchModelRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		log.Warnf("fetch model request bind failed: %v", err)
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel, err := request.toChannel()
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	models, err := helper.FetchModels(c.Request.Context(), channel)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, models)
}

func syncChannel(c *gin.Context) {
	task.SyncModelsTask()
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	time := task.GetLastSyncModelsTime()
	resp.Success(c, time)
}
