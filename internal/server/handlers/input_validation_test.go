package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/gin-gonic/gin"
)

func TestAdminCRUDHandlersRejectInvalidBusinessInput(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "channel unknown type", body: `{"name":"channel","type":"unknown","base_urls":[{"url":"https://example.com"}],"keys":[{"channel_key":"secret"}]}`, handler: createChannel},
		{name: "channel server-owned id", body: `{"id":7,"name":"channel","type":"openai/chat_completions","base_urls":[{"url":"https://example.com"}],"keys":[{"channel_key":"secret"}]}`, handler: createChannel},
		{name: "channel key runtime state", body: `{"name":"channel","type":"openai/chat_completions","base_urls":[{"url":"https://example.com"}],"keys":[{"channel_key":"secret","total_cost":100}]}`, handler: createChannel},
		{name: "channel invalid override", body: `{"name":"channel","type":"openai/chat_completions","base_urls":[{"url":"https://example.com"}],"keys":[{"channel_key":"secret"}],"param_override":"[]"}`, handler: createChannel},
		{name: "channel negative RPM", body: `{"id":1,"rpm_limit":-1}`, handler: updateChannel},
		{name: "group blank name", body: `{"name":" ","mode":1}`, handler: createGroup},
		{name: "group item server-owned id", body: `{"name":"group","mode":1,"items":[{"id":9,"type":"channel","channel_id":1,"model_name":"m","weight":1}]}`, handler: createGroup},
		{name: "group unknown mode", body: `{"name":"group","mode":0}`, handler: createGroup},
		{name: "group negative timeout", body: `{"id":1,"first_token_time_out":-1}`, handler: updateGroup},
		{name: "group negative weight", body: `{"id":1,"items_to_add":[{"type":"channel","channel_id":1,"model_name":"m","weight":-1}]}`, handler: updateGroup},
		{name: "API key blank name", body: `{"name":" "}`, handler: createAPIKey},
		{name: "API key supplied secret", body: `{"name":"key","api_key":"attacker-selected"}`, handler: createAPIKey},
		{name: "API key negative cost", body: `{"name":"key","max_cost":-1}`, handler: createAPIKey},
		{name: "API key negative expiry", body: `{"id":1,"name":"key","expire_at":-1}`, handler: updateAPIKey},
		{name: "model blank name", body: `{"name":" "}`, handler: createLLM},
		{name: "model negative price", body: `{"name":"model","input":-1}`, handler: updateLLM},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/test", test.body, test.handler)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func TestAPIKeyPartialUpdatePreservesOmittedFieldsAndAllowsExplicitClear(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "api-key-partial.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache() error = %v", err)
	}
	key := model.APIKey{
		Name:            "before",
		APIKey:          "generated-secret",
		Enabled:         true,
		ExpireAt:        2_000_000_000,
		MaxCost:         42,
		SupportedModels: "gpt-5",
	}
	if err := op.APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() error = %v", err)
	}

	partial := invokeHandler(http.MethodPost, "/test", `{"id":`+jsonInt(key.ID)+`,"name":"after"}`, updateAPIKey)
	if partial.Code != http.StatusOK {
		t.Fatalf("partial update status = %d; body=%s", partial.Code, partial.Body.String())
	}
	persisted, err := op.APIKeyGet(key.ID, context.Background())
	if err != nil {
		t.Fatalf("APIKeyGet() error = %v", err)
	}
	if persisted.Name != "after" || !persisted.Enabled || persisted.ExpireAt != key.ExpireAt || persisted.MaxCost != 42 || persisted.SupportedModels != "gpt-5" {
		t.Fatalf("omitted fields changed: %#v", persisted)
	}

	clear := invokeHandler(http.MethodPost, "/test", `{"id":`+jsonInt(key.ID)+`,"expire_at":0,"max_cost":0,"supported_models":""}`, updateAPIKey)
	if clear.Code != http.StatusOK {
		t.Fatalf("clear update status = %d; body=%s", clear.Code, clear.Body.String())
	}
	persisted, err = op.APIKeyGet(key.ID, context.Background())
	if err != nil {
		t.Fatalf("APIKeyGet() after clear error = %v", err)
	}
	if persisted.ExpireAt != 0 || persisted.MaxCost != 0 || persisted.SupportedModels != "" {
		t.Fatalf("explicit clear was not persisted: %#v", persisted)
	}
}

func jsonInt(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestAdminCRUDHandlersMapMissingResourcesToNotFound(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "not-found.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache() error = %v", err)
	}

	tests := []struct {
		name    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "channel", body: `{"id":999999}`, handler: updateChannel},
		{name: "group", body: `{"id":999999}`, handler: updateGroup},
		{name: "API key", body: `{"id":999999,"name":"missing"}`, handler: updateAPIKey},
		{name: "model", body: `{"name":"missing"}`, handler: updateLLM},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/test", test.body, test.handler)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNotFound, response.Body.String())
			}
		})
	}
}
