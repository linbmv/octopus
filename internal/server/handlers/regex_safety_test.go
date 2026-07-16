package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestChannelAndGroupHandlersRejectOverlongRegex(t *testing.T) {
	pattern := strings.Repeat("a", helper.ModelRegexMaxPatternBytes+1)
	tests := []struct {
		name    string
		payload any
		handler gin.HandlerFunc
	}{
		{
			name:    "create channel",
			payload: model.Channel{Name: "channel", MatchRegex: &pattern},
			handler: createChannel,
		},
		{
			name:    "update channel",
			payload: model.ChannelUpdateRequest{ID: 1, MatchRegex: &pattern},
			handler: updateChannel,
		},
		{
			name:    "create group",
			payload: model.Group{Name: "group", MatchRegex: pattern},
			handler: createGroup,
		},
		{
			name:    "update group",
			payload: model.GroupUpdateRequest{ID: 1, MatchRegex: &pattern},
			handler: updateGroup,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			response := invokeRegexSafetyHandler(body, tt.handler)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func TestFetchModelRequestRejectsOverlongRegex(t *testing.T) {
	pattern := strings.Repeat("a", helper.ModelRegexMaxPatternBytes+1)
	_, err := (fetchModelRequest{
		Type:       llm.APIFormatOpenAIChatCompletion,
		BaseUrls:   []model.BaseUrl{{URL: "https://models.example"}},
		Keys:       []fetchModelKey{{ChannelKey: "key"}},
		MatchRegex: &pattern,
	}).toChannel()
	if err == nil || !strings.Contains(err.Error(), "match_regex is invalid") {
		t.Fatalf("toChannel error = %v, want invalid regex", err)
	}
}

func invokeRegexSafetyHandler(body []byte, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test", handler)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	return response
}
