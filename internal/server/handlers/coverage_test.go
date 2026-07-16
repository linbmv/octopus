package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRespondInternalErrorKeepsDetailsOutOfResponseAndLogsFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	core, observed := observer.New(zap.ErrorLevel)
	previousLogger := log.Logger
	log.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { log.Logger = previousLogger })

	const sentinel = "postgres password=must-not-reach-client"
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	respondInternalError(context, "list API models failed", errors.New(sentinel))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), sentinel) {
		t.Fatalf("internal error leaked in response: %s", response.Body.String())
	}
	var body resp.ResponseStruct
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Message != resp.ErrInternalServer || body.Error == nil || body.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("internal error response = %#v", body)
	}
	if observed.Len() != 1 || observed.All()[0].Message != "list API models failed" {
		t.Fatalf("observed internal-error logs = %#v", observed.All())
	}
}

func TestJSONHandlersRejectInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := map[string]gin.HandlerFunc{
		"create channel":  createChannel,
		"update channel":  updateChannel,
		"enable channel":  enableChannel,
		"create group":    createGroup,
		"update group":    updateGroup,
		"create API key":  createAPIKey,
		"update API key":  updateAPIKey,
		"create LLM":      createLLM,
		"update LLM":      updateLLM,
		"delete LLM":      deleteLLM,
		"login":           login,
		"change password": changePassword,
		"change username": changeUsername,
		"set setting":     setSetting,
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/test", `{"broken":`, handler)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func TestDeleteHandlersRejectInvalidID(t *testing.T) {
	tests := map[string]gin.HandlerFunc{
		"channel": deleteChannel,
		"group":   deleteGroup,
		"API key": deleteAPIKey,
	}
	for name, handler := range tests {
		for _, id := range []string{"invalid", "0"} {
			t.Run(name+"/"+id, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				router := gin.New()
				router.DELETE("/:id", handler)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/"+id, nil))
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
				}
			})
		}
	}
}

func TestGroupHandlersRejectInvalidRegex(t *testing.T) {
	createResponse := invokeHandler(http.MethodPost, "/group", `{"name":"bad","match_regex":"("}`, createGroup)
	if createResponse.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want %d", createResponse.Code, http.StatusBadRequest)
	}
	updateResponse := invokeHandler(http.MethodPost, "/group", `{"id":1,"match_regex":"("}`, updateGroup)
	if updateResponse.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want %d", updateResponse.Code, http.StatusBadRequest)
	}
}

func TestFetchModelRequestValidation(t *testing.T) {
	disabled := false
	validType := llm.APIFormatOpenAIChatCompletion
	validURL := model.BaseUrl{URL: "https://example.com", Delay: 0}
	validKey := fetchModelKey{ChannelKey: "key"}
	invalidRegex := "("
	tests := []struct {
		name    string
		request fetchModelRequest
		want    string
	}{
		{name: "missing type", request: fetchModelRequest{}, want: "channel type is required"},
		{name: "negative delay", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{{URL: "https://example.com", Delay: -1}}}, want: "delay"},
		{name: "invalid URL", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{{URL: "not-a-url"}}}, want: "invalid URL"},
		{name: "missing base URL", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{{URL: " "}}}, want: "base_urls is required"},
		{name: "missing key", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{validURL}}, want: "keys is required"},
		{name: "disabled keys", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{validURL}, Keys: []fetchModelKey{{Enabled: &disabled, ChannelKey: "key"}}}, want: "enabled API key"},
		{name: "invalid regex", request: fetchModelRequest{Type: validType, BaseUrls: []model.BaseUrl{validURL}, Keys: []fetchModelKey{validKey}, MatchRegex: &invalidRegex}, want: "match_regex is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.request.toChannel()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("toChannel() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeDBDumpSupportsRawAndWrappedPayloads(t *testing.T) {
	for name, body := range map[string]string{
		"raw":     `{"version":1,"channels":[{"name":"channel"}]}`,
		"wrapped": `{"code":0,"data":{"version":2,"groups":[{"name":"group"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var dump model.DBDump
			if err := decodeDBDump([]byte(body), &dump); err != nil {
				t.Fatalf("decodeDBDump() error = %v", err)
			}
			if dump.Version == 0 {
				t.Fatal("decoded dump has zero version")
			}
		})
	}
	if err := decodeDBDump([]byte(`{}`), nil); err != nil {
		t.Fatalf("decodeDBDump(nil target) error = %v", err)
	}
	var dump model.DBDump
	if err := decodeDBDump([]byte(`{"version":`), &dump); err == nil {
		t.Fatal("decodeDBDump() expected invalid JSON error")
	}
}

func TestSimpleHandlers(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		handler gin.HandlerFunc
		status  int
	}{
		{name: "active requests", method: http.MethodGet, path: "/active", handler: GetActiveRequests, status: http.StatusOK},
		{name: "active request count", method: http.MethodGet, path: "/active/count", handler: GetActiveRequestsCount, status: http.StatusOK},
		{name: "liveness", method: http.MethodGet, path: "/liveness", handler: livenessCheck, status: http.StatusOK},
		{name: "user status", method: http.MethodGet, path: "/status", handler: status, status: http.StatusOK},
		{name: "current version", method: http.MethodGet, path: "/version", handler: getNowVersion, status: http.StatusOK},
		{name: "API key login", method: http.MethodGet, path: "/login", handler: loginAPIKey, status: http.StatusOK},
		{name: "last sync time", method: http.MethodGet, path: "/sync-time", handler: getLastSyncTime, status: http.StatusOK},
		{name: "last price update", method: http.MethodGet, path: "/price-time", handler: getLastUpdateTime, status: http.StatusOK},
		{name: "invalid stream token", method: http.MethodGet, path: "/stream", handler: streamLog, status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := invokeHandler(test.method, test.path, "", test.handler)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}

	gemini := newGeminiModel("gemini-test")
	if gemini.Name != "models/gemini-test" || gemini.DisplayName != "gemini-test" {
		t.Fatalf("newGeminiModel() = %#v", gemini)
	}
}

func TestDatabaseBackedHandlers(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "handlers.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache() error = %v", err)
	}

	getters := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "health", path: "/health", handler: healthCheck},
		{name: "readiness", path: "/ready", handler: readinessCheck},
		{name: "channels", path: "/channels", handler: listChannel},
		{name: "groups", path: "/groups", handler: getGroupList},
		{name: "API keys", path: "/api-keys", handler: listAPIKey},
		{name: "settings", path: "/settings", handler: getSettingList},
		{name: "LLMs", path: "/llms", handler: listLLM},
		{name: "channel LLMs", path: "/channel-llms", handler: listLLMByChannel},
		{name: "stats today", path: "/stats/today", handler: getStatsToday},
		{name: "stats daily", path: "/stats/daily", handler: getStatsDaily},
		{name: "stats hourly", path: "/stats/hourly", handler: getStatsHourly},
		{name: "stats total", path: "/stats/total", handler: getStatsTotal},
		{name: "stats API keys", path: "/stats/api-keys", handler: getStatsAPIKey},
		{name: "stats error levels", path: "/stats/error-levels?window_hours=24&channel_id=0", handler: getStatsErrorLevels},
		{name: "logs", path: "/logs?page=0&page_size=1000", handler: listLog},
		{name: "stream token", path: "/stream-token", handler: getStreamToken},
	}
	for _, getter := range getters {
		t.Run(getter.name, func(t *testing.T) {
			response := invokeHandler(http.MethodGet, getter.path, "", getter.handler)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}
		})
	}

	createGroupResponse := invokeHandler(http.MethodPost, "/group", `{"name":"group-test","mode":1}`, createGroup)
	if createGroupResponse.Code != http.StatusOK {
		t.Fatalf("create group status = %d; body=%s", createGroupResponse.Code, createGroupResponse.Body.String())
	}
	var group model.Group
	decodeResponseData(t, createGroupResponse, &group)
	updateGroupResponse := invokeHandler(http.MethodPost, "/group", `{"id":`+jsonNumber(group.ID)+`,"name":"group-updated"}`, updateGroup)
	if updateGroupResponse.Code != http.StatusOK {
		t.Fatalf("update group status = %d; body=%s", updateGroupResponse.Code, updateGroupResponse.Body.String())
	}
	deleteGroupResponse := invokeHandler(http.MethodDelete, "/group/"+jsonNumber(group.ID), "", routeWithID(deleteGroup, group.ID))
	if deleteGroupResponse.Code != http.StatusOK {
		t.Fatalf("delete group status = %d; body=%s", deleteGroupResponse.Code, deleteGroupResponse.Body.String())
	}

	createKeyResponse := invokeHandler(http.MethodPost, "/api-key", `{"name":"key-test","enabled":true}`, createAPIKey)
	if createKeyResponse.Code != http.StatusOK {
		t.Fatalf("create API key status = %d; body=%s", createKeyResponse.Code, createKeyResponse.Body.String())
	}
	var apiKey model.APIKey
	decodeResponseData(t, createKeyResponse, &apiKey)
	updateKeyResponse := invokeHandler(http.MethodPost, "/api-key", `{"id":`+jsonNumber(apiKey.ID)+`,"name":"key-updated","enabled":true}`, updateAPIKey)
	if updateKeyResponse.Code != http.StatusOK {
		t.Fatalf("update API key status = %d; body=%s", updateKeyResponse.Code, updateKeyResponse.Body.String())
	}
	deleteKeyResponse := invokeHandler(http.MethodDelete, "/api-key/"+jsonNumber(apiKey.ID), "", routeWithID(deleteAPIKey, apiKey.ID))
	if deleteKeyResponse.Code != http.StatusOK {
		t.Fatalf("delete API key status = %d; body=%s", deleteKeyResponse.Code, deleteKeyResponse.Body.String())
	}

	for _, operation := range []struct {
		name    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "create LLM", body: `{"name":"gpt-test"}`, handler: createLLM},
		{name: "update LLM", body: `{"name":"gpt-test","input":1.5}`, handler: updateLLM},
		{name: "delete LLM", body: `{"name":"gpt-test"}`, handler: deleteLLM},
		{name: "set setting", body: `{"key":"cors_allow_origins","value":"https://example.com"}`, handler: setSetting},
	} {
		t.Run(operation.name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/operation", operation.body, operation.handler)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}
		})
	}

	exportResponse := invokeHandler(http.MethodGet, "/export?include_logs=true&include_stats=true", "", exportDB)
	if exportResponse.Code != http.StatusOK || !strings.Contains(exportResponse.Body.String(), `"version"`) {
		t.Fatalf("export response = %d %s", exportResponse.Code, exportResponse.Body.String())
	}
	importResponse := invokeHandler(http.MethodPost, "/import", `{"version":1,"exported_at":"2026-07-15T12:00:00Z","include_logs":false,"include_stats":false}`, importDB)
	if importResponse.Code != http.StatusOK {
		t.Fatalf("import status = %d; body=%s", importResponse.Code, importResponse.Body.String())
	}
	clearResponse := invokeHandler(http.MethodDelete, "/logs", "", clearLog)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clear logs status = %d; body=%s", clearResponse.Code, clearResponse.Body.String())
	}
}

func invokeHandler(method, path, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	routePath := strings.SplitN(path, "?", 2)[0]
	router.Handle(method, routePath, handler)
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func routeWithID(handler gin.HandlerFunc, id int) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Params = append(c.Params, gin.Param{Key: "id", Value: jsonNumber(id)})
		handler(c)
	}
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func decodeResponseData(t *testing.T, response *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response envelope: %v; body=%s", err, response.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode response data: %v; body=%s", err, response.Body.String())
	}
}
