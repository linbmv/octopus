package handlers

import (
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func init() {
	router.NewGroupRouter("/api/v1/model").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listLLM),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createLLM),
		).
		AddRoute(
			router.NewRoute("/channel", http.MethodGet).
				Handle(listLLMByChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateLLM),
		).
		AddRoute(
			router.NewRoute("/delete", http.MethodPost).
				Handle(deleteLLM),
		).
		AddRoute(
			router.NewRoute("/update-price", http.MethodPost).
				Handle(updateLLMPrice),
		).
		AddRoute(
			router.NewRoute("/last-update-time", http.MethodGet).
				Handle(getLastUpdateTime),
		)
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/models", http.MethodGet).
				Handle(getModelList),
		)
	router.NewGroupRouter("/v1beta").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/models", http.MethodGet).
				Handle(getModelList),
		).
		AddRoute(
			router.NewRoute("/models/*action", http.MethodGet).
				Handle(getGeminiModel),
		)
}

func getModelList(c *gin.Context) {
	models, err := availableModelsForAPIKey(c)
	if err != nil {
		respondInternalError(c, "list API models failed", err)
		return
	}

	if c.GetString("request_type") == "gemini" {
		geminiModels := lo.Map(models, func(m string, _ int) model.GeminiModel {
			return newGeminiModel(m)
		})
		c.JSON(200, model.GeminiModelList{Models: geminiModels})
		return
	}

	if c.GetString("request_type") == "anthropic" {
		var anthropicModels []model.AnthropicModel
		for _, m := range models {
			anthropicModels = append(anthropicModels, model.AnthropicModel{
				ID:          m,
				CreatedAt:   "2024-01-01T00:00:00Z",
				DisplayName: m,
				Type:        "model",
			})
		}
		response := gin.H{
			"data":     anthropicModels,
			"has_more": false,
		}
		if len(anthropicModels) > 0 {
			response["first_id"] = anthropicModels[0].ID
			response["last_id"] = anthropicModels[len(anthropicModels)-1].ID
		}
		c.JSON(200, response)
	} else {
		var openAIModels []model.OpenAIModel
		for _, m := range models {
			openAIModels = append(openAIModels, model.OpenAIModel{
				ID:      m,
				Object:  "model",
				Created: 1763395200,
				OwnedBy: "octopus",
			})
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    openAIModels,
			"object":  "list",
		})
	}
}

func getGeminiModel(c *gin.Context) {
	requestedModel := strings.TrimPrefix(c.Param("action"), "/")
	requestedModel = strings.TrimPrefix(requestedModel, "models/")
	if requestedModel == "" {
		resp.Error(c, http.StatusNotFound, resp.ErrResourceNotFound)
		return
	}

	models, err := availableModelsForAPIKey(c)
	if err != nil {
		respondInternalError(c, "get Gemini API model failed", err)
		return
	}
	matchedModel := ""
	for _, m := range models {
		if strings.EqualFold(m, requestedModel) {
			matchedModel = m
			break
		}
	}
	if matchedModel == "" {
		resp.Error(c, http.StatusNotFound, resp.ErrResourceNotFound)
		return
	}

	c.JSON(200, newGeminiModel(matchedModel))
}

func availableModelsForAPIKey(c *gin.Context) ([]string, error) {
	models, err := op.GroupListModel(c.Request.Context())
	if err != nil {
		return nil, err
	}
	apiKeyId := c.GetInt("api_key_id")
	apiKey, err := op.APIKeyGet(apiKeyId, c.Request.Context())
	if err != nil {
		return nil, err
	}
	if apiKey.SupportedModels == "" {
		return models, nil
	}

	supportedModels := lo.Map(strings.Split(apiKey.SupportedModels, ","), func(s string, _ int) string {
		return strings.TrimSpace(s)
	})
	return lo.Filter(models, func(m string, _ int) bool {
		return lo.Contains(supportedModels, m)
	}), nil
}

func newGeminiModel(name string) model.GeminiModel {
	return model.GeminiModel{
		Name:                       "models/" + name,
		Version:                    "001",
		DisplayName:                name,
		Description:                name,
		InputTokenLimit:            1048576,
		OutputTokenLimit:           65536,
		SupportedGenerationMethods: []string{"generateContent", "countTokens"},
	}
}

func listLLM(c *gin.Context) {
	models, err := op.LLMList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list models failed", err)
		return
	}
	resp.Success(c, models)
}

func listLLMByChannel(c *gin.Context) {
	channels, err := op.ChannelLLMList(c.Request.Context())
	if err != nil {
		respondInternalError(c, "list channel models failed", err)
		return
	}
	resp.Success(c, channels)
}

func createLLM(c *gin.Context) {
	var info model.LLMInfo
	if err := bindStrictJSON(c, &info); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := model.ValidateLLMInfo(&info); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.LLMCreate(info, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	resp.Success(c, info)
}

func updateLLM(c *gin.Context) {
	var info model.LLMInfo
	if err := bindStrictJSON(c, &info); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := model.ValidateLLMInfo(&info); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := op.LLMUpdate(info, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	resp.Success(c, info)
}

func deleteLLM(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := bindStrictJSON(c, &req); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	probe := model.LLMInfo{Name: req.Name}
	if err := model.ValidateLLMInfo(&probe); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = probe.Name
	if err := op.LLMDelete(req.Name, c.Request.Context()); err != nil {
		respondOperationError(c, err)
		return
	}
	resp.Success(c, nil)
}

func updateLLMPrice(c *gin.Context) {
	err := price.UpdateLLMPrice(c.Request.Context())
	if err != nil {
		respondInternalError(c, "update model prices failed", err)
		return
	}
	resp.Success(c, nil)
}

func getLastUpdateTime(c *gin.Context) {
	time := price.GetLastUpdateTime()
	resp.Success(c, time)
}
