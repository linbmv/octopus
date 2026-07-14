package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestFetchModelAcceptsFormPayloadWithoutChannelName(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "gpt-4o"}},
		})
	}))
	defer upstream.Close()

	payload := map[string]any{
		"type": llm.APIFormatOpenAIChatCompletion,
		"base_urls": []map[string]any{
			{"url": upstream.URL, "delay": 0},
			{"url": " ", "delay": 0},
		},
		"keys": []map[string]any{
			{"enabled": true, "channel_key": " test-key "},
			{"enabled": true, "channel_key": " "},
		},
		"proxy":         false,
		"channel_proxy": " ",
		"match_regex":   " ",
		"custom_header": []map[string]any{
			{"header_key": "", "header_value": "ignored"},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	router := gin.New()
	router.POST("/fetch-model", fetchModel)

	req := httptest.NewRequest(http.MethodPost, "/fetch-model", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if gotPath != "/v1/models" {
		t.Fatalf("upstream path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", gotAuth)
	}

	var response struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0] != "gpt-4o" {
		t.Fatalf("models = %v, want [gpt-4o]", response.Data)
	}
}
