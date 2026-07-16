package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/capability"
	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/bestruirui/octopus/internal/utils/log"
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
		).
		AddRoute(
			router.NewRoute("/:id/capabilities/probe", http.MethodPost).
				Handle(probeChannelCapabilities),
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
		).
		AddRoute(
			router.NewRoute("/:id/capabilities", http.MethodGet).
				Handle(listChannelCapabilities),
		)
}

type channelCapabilityEvidence struct {
	model.CapabilityEvidence
	KeyRemark  string `json:"key_remark,omitempty"`
	KeyEnabled bool   `json:"key_enabled"`
	Fresh      bool   `json:"fresh"`
}

func listChannelCapabilities(c *gin.Context) {
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
	evidence, err := op.CapabilityEvidenceList(c.Request.Context(), channelID)
	if err != nil {
		respondInternalError(c, "list channel capability evidence failed", err)
		return
	}
	keys := make(map[int]model.ChannelKey, len(channel.Keys))
	for _, key := range channel.Keys {
		keys[key.ID] = key
	}
	configuredEndpoints := make(map[string]struct{}, len(channel.BaseUrls))
	for _, endpoint := range channel.BaseUrls {
		configuredEndpoints[strings.TrimSpace(endpoint.URL)] = struct{}{}
	}
	now := time.Now().UTC()
	result := make([]channelCapabilityEvidence, 0, len(evidence))
	for _, item := range evidence {
		key, keyExists := keys[item.ChannelKeyID]
		_, endpointExists := configuredEndpoints[item.Endpoint]
		fresh := keyExists && endpointExists && item.ExpiresAt.After(now) &&
			item.ScopeFingerprint == model.CapabilityScopeFingerprint(channel, key, item.Endpoint)
		result = append(result, channelCapabilityEvidence{
			CapabilityEvidence: item,
			KeyRemark:          key.Remark,
			KeyEnabled:         key.Enabled,
			Fresh:              fresh,
		})
	}
	resp.Success(c, result)
}

func probeChannelCapabilities(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	var request struct {
		Models       []string           `json:"models,omitempty"`
		Capabilities []model.Capability `json:"capabilities,omitempty"`
		MaxCostUSD   float64            `json:"max_cost_usd,omitempty"`
	}
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	worker := capability.DefaultWorker()
	if worker == nil {
		resp.ErrorWithCode(c, http.StatusServiceUnavailable, "CAPABILITY_PROBE_UNAVAILABLE", "capability probe worker is unavailable")
		return
	}
	report, err := worker.Submit(c.Request.Context(), capability.SubmitRequest{
		ChannelID: channelID, Models: request.Models,
		Capabilities: request.Capabilities, MaxCostUSD: request.MaxCostUSD,
	})
	if errors.Is(err, capability.ErrProbeDisabled) {
		resp.ErrorWithCode(c, http.StatusConflict, "CAPABILITY_PROBE_DISABLED", "capability probing is disabled")
		return
	}
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditLog(c, middleware.EventChannelCapabilityProbe, map[string]interface{}{
		"channel_id": channelID,
		"requested":  report.Requested,
		"accepted":   report.Accepted,
		"cost_usd":   report.ReservedCostUSD,
	})
	resp.Success(c, report)
}

func listChannel(c *gin.Context) {
	channels, err := op.ChannelList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list channels failed", err)
		return
	}
	for i, channel := range channels {
		stats := op.StatsChannelGet(channel.ID)
		channels[i].Stats = &stats
	}
	resp.Success(c, channels)
}

type channelCreateRequest struct {
	Name             string                       `json:"name"`
	Type             llm.APIFormat                `json:"type"`
	Enabled          *bool                        `json:"enabled,omitempty"`
	BaseUrls         []model.BaseUrl              `json:"base_urls"`
	Keys             []model.ChannelKeyAddRequest `json:"keys"`
	Model            string                       `json:"model"`
	CustomModel      string                       `json:"custom_model,omitempty"`
	Proxy            bool                         `json:"proxy,omitempty"`
	AutoSync         bool                         `json:"auto_sync,omitempty"`
	AutoGroup        model.AutoGroupType          `json:"auto_group,omitempty"`
	CustomHeader     []model.CustomHeader         `json:"custom_header,omitempty"`
	HeaderRules      []model.HeaderRule           `json:"header_rules,omitempty"`
	JSONRewriteRules []model.JSONRewriteRule      `json:"json_rewrite_rules,omitempty"`
	ParamOverride    *string                      `json:"param_override,omitempty"`
	RawPassthrough   bool                         `json:"raw_passthrough,omitempty"`
	RPMLimit         int                          `json:"rpm_limit,omitempty"`
	MaxConcurrency   int                          `json:"max_concurrency,omitempty"`
	ChannelProxy     *string                      `json:"channel_proxy,omitempty"`
	MatchRegex       *string                      `json:"match_regex,omitempty"`
	UserAgent        string                       `json:"user_agent,omitempty"`
}

func (r channelCreateRequest) channel() model.Channel {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	keys := make([]model.ChannelKey, len(r.Keys))
	for i, key := range r.Keys {
		keys[i] = model.ChannelKey{Enabled: key.Enabled, ChannelKey: key.ChannelKey, Remark: key.Remark}
	}
	return model.Channel{
		Name:             r.Name,
		Type:             r.Type,
		Enabled:          enabled,
		BaseUrls:         r.BaseUrls,
		Keys:             keys,
		Model:            r.Model,
		CustomModel:      r.CustomModel,
		Proxy:            r.Proxy,
		AutoSync:         r.AutoSync,
		AutoGroup:        r.AutoGroup,
		CustomHeader:     r.CustomHeader,
		HeaderRules:      r.HeaderRules,
		JSONRewriteRules: r.JSONRewriteRules,
		ParamOverride:    r.ParamOverride,
		RawPassthrough:   r.RawPassthrough,
		RPMLimit:         r.RPMLimit,
		MaxConcurrency:   r.MaxConcurrency,
		ChannelProxy:     r.ChannelProxy,
		MatchRegex:       r.MatchRegex,
		UserAgent:        r.UserAgent,
	}
}

func createChannel(c *gin.Context) {
	var request channelCreateRequest
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	channel := request.channel()
	if err := model.ValidateChannel(&channel); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateOptionalModelMatchRegex(channel.MatchRegex); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.ChannelCreate(&channel, c.Request.Context()); err != nil {
		respondOperationError(c, err)
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
	if err := bindStrictJSON(c, &req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := model.ValidateChannelUpdate(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateOptionalModelMatchRegex(req.MatchRegex); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	oldChannel, _ := op.ChannelGet(req.ID, c.Request.Context())
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		respondOperationError(c, err)
		return
	}
	invalidateChannelRuntimeState(channel.ID, oldChannel, channel)
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
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if request.ID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	channel, _ := op.ChannelGet(request.ID, c.Request.Context())
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	invalidateChannelRuntimeState(request.ID, channel)
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil || idNum <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	channel, _ := op.ChannelGet(idNum, c.Request.Context())
	if err := op.ChannelDel(idNum, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	invalidateChannelRuntimeState(idNum, channel)
	middleware.AuditLog(c, middleware.EventChannelDelete, map[string]interface{}{
		"channel_id": idNum,
	})
	resp.Success(c, nil)
}

func invalidateChannelRuntimeState(channelID int, channels ...*model.Channel) {
	balancer.InvalidateChannel(channelID)
	relay.InvalidateRuntimeURLState(channelID)

	seenProxyURLs := make(map[string]struct{}, len(channels))
	foundChannel := false
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		foundChannel = true
		if channel.ChannelProxy == nil {
			continue
		}
		proxyURL := strings.TrimSpace(*channel.ChannelProxy)
		if proxyURL == "" {
			continue
		}
		if _, exists := seenProxyURLs[proxyURL]; exists {
			continue
		}
		seenProxyURLs[proxyURL] = struct{}{}
		client.InvalidateCustomProxyClient(proxyURL)
	}
	if !foundChannel {
		// A concurrent cache refresh can make the pre-change snapshot unavailable.
		// Clearing this small bounded cache is safer than retaining an obsolete
		// authenticated proxy transport whose previous URL is no longer known.
		client.InvalidateAllCustomProxyClients()
	}
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
	if err := model.ValidateChannelType(r.Type); err != nil {
		return model.Channel{}, err
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
		if err := validateModelMatchRegex(*matchRegex); err != nil {
			return model.Channel{}, errors.New("match_regex is invalid")
		}
	}

	channel := model.Channel{
		Name:         "fetch-model",
		Type:         r.Type,
		BaseUrls:     baseUrls,
		Keys:         keys,
		Proxy:        r.Proxy,
		ChannelProxy: trimOptionalString(r.ChannelProxy),
		MatchRegex:   matchRegex,
		CustomHeader: trimCustomHeaders(r.CustomHeaders),
	}
	if err := model.ValidateChannel(&channel); err != nil {
		return model.Channel{}, err
	}
	return channel, nil
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
	if err := bindStrictJSON(c, &request); err != nil {
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
		respondInternalError(c, "fetch channel models failed", err)
		return
	}
	resp.Success(c, models)
}

func syncChannel(c *gin.Context) {
	if err := task.SyncModelsNow(c.Request.Context()); err != nil {
		if errors.Is(err, task.ErrSyncModelsInProgress) {
			resp.Error(c, http.StatusConflict, err.Error())
		} else {
			resp.Error(c, http.StatusBadGateway, "model synchronization failed")
		}
		return
	}
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	time := task.GetLastSyncModelsTime()
	resp.Success(c, time)
}
