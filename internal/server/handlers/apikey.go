package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func init() {
	router.NewGroupRouter("/api/v1/apikey").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createAPIKey),
		).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listAPIKey),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateAPIKey),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteAPIKey),
		)
	router.NewGroupRouter("/api/v1/apikey").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/stats", http.MethodGet).
				Handle(getStatsAPIKeyById),
		).
		AddRoute(
			router.NewRoute("/login", http.MethodGet).
				Handle(loginAPIKey),
		)
}

type apiKeyCreateRequest struct {
	Name            string  `json:"name"`
	Enabled         *bool   `json:"enabled,omitempty"`
	ExpireAt        int64   `json:"expire_at,omitempty"`
	MaxCost         float64 `json:"max_cost,omitempty"`
	SupportedModels string  `json:"supported_models,omitempty"`
}

func (r apiKeyCreateRequest) apiKey() model.APIKey {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return model.APIKey{
		Name:            r.Name,
		Enabled:         enabled,
		ExpireAt:        r.ExpireAt,
		MaxCost:         r.MaxCost,
		SupportedModels: r.SupportedModels,
	}
}

type apiKeyUpdateRequest struct {
	ID              int      `json:"id"`
	Name            *string  `json:"name,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
	ExpireAt        *int64   `json:"expire_at,omitempty"`
	MaxCost         *float64 `json:"max_cost,omitempty"`
	SupportedModels *string  `json:"supported_models,omitempty"`
}

func (r apiKeyUpdateRequest) apply(existing model.APIKey) model.APIKey {
	if r.Name != nil {
		existing.Name = *r.Name
	}
	if r.Enabled != nil {
		existing.Enabled = *r.Enabled
	}
	if r.ExpireAt != nil {
		existing.ExpireAt = *r.ExpireAt
	}
	if r.MaxCost != nil {
		existing.MaxCost = *r.MaxCost
	}
	if r.SupportedModels != nil {
		existing.SupportedModels = *r.SupportedModels
	}
	return existing
}

func (r apiKeyUpdateRequest) validate() error {
	probe := r.apply(model.APIKey{Name: "validation-placeholder"})
	return model.ValidateAPIKey(&probe)
}

func createAPIKey(c *gin.Context) {
	var request apiKeyCreateRequest
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	req := request.apiKey()
	if err := model.ValidateAPIKey(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	apiKey, err := auth.GenerateAPIKey()
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	req.APIKey = apiKey
	if err := op.APIKeyCreate(&req, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	resp.Success(c, req)
}

func listAPIKey(c *gin.Context) {
	apiKeys, err := op.APIKeyList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list API keys failed", err)
		return
	}
	resp.Success(c, apiKeys)
}

func updateAPIKey(c *gin.Context) {
	var request apiKeyUpdateRequest
	if err := bindStrictJSON(c, &request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if request.ID <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := request.validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := op.APIKeyGet(request.ID, c.Request.Context())
	if err != nil {
		respondOperationError(c, err)
		return
	}
	req := request.apply(existing)
	if err := model.ValidateAPIKey(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.APIKeyUpdate(&req, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	balancer.InvalidateAPIKey(req.ID)
	resp.Success(c, req)
}

func deleteAPIKey(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil || idNum <= 0 {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := op.APIKeyDelete(idNum, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	balancer.InvalidateAPIKey(idNum)
	resp.Success(c, nil)
}

func getStatsAPIKeyById(c *gin.Context) {
	id := c.GetInt("api_key_id")
	stats := op.StatsAPIKeyGet(id)
	info, err := op.APIKeyGet(id, c.Request.Context())
	if err != nil {
		respondInternalError(c, "get API key statistics metadata failed", err)
		return
	}
	models, err := op.GroupListModel(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list API key statistics models failed", err)
		return
	}
	var modelsString string
	if info.SupportedModels == "" {
		modelsString = strings.Join(models, ", ")
	} else {
		supportedModels := lo.Map(strings.Split(info.SupportedModels, ","), func(s string, _ int) string {
			return strings.TrimSpace(s)
		})
		models = lo.Filter(models, func(m string, _ int) bool {
			return lo.Contains(supportedModels, m)
		})
		modelsString = strings.Join(models, ", ")
	}
	info.SupportedModels = modelsString
	resp.Success(c, map[string]any{
		"stats": stats,
		"info":  info,
	})
}

func loginAPIKey(c *gin.Context) {
	resp.Success(c, nil)
}
