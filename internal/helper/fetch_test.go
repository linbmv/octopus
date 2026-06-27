package helper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/looplj/axonhub/llm"
)

// testChannel 构造一个直连（不走代理）的渠道，BaseUrl 指向测试 server，并带一个可用 key。
func testChannel(baseURL string, channelType llm.APIFormat) model.Channel {
	return model.Channel{
		Type:     channelType,
		Proxy:    false,
		BaseUrls: []model.BaseUrl{{URL: baseURL}},
		Keys:     []model.ChannelKey{{ID: 1, Enabled: true, ChannelKey: "test-key"}},
	}
}

func TestChannelAutoGroupPrunesStaleItemsWhenAutoGroupDisabled(t *testing.T) {
	ctx := context.Background()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "octopus.db"), false); err != nil {
		t.Fatalf("init test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	group := model.Group{ID: 701, Name: "manual-group", Enabled: true, Mode: model.GroupModeFailover}
	if err := db.GetDB().WithContext(ctx).Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	items := []model.GroupItem{
		{ID: 7001, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 801, ModelName: "old-model", Priority: 1},
		{ID: 7002, GroupID: group.ID, Type: model.GroupItemTypeChannel, ChannelID: 801, ModelName: "current-model", Priority: 2},
	}
	if err := db.GetDB().WithContext(ctx).Create(&items).Error; err != nil {
		t.Fatalf("create group items: %v", err)
	}
	if err := op.GroupRefreshCacheByID(group.ID, ctx); err != nil {
		t.Fatalf("refresh group cache: %v", err)
	}

	ChannelAutoGroup(&model.Channel{
		ID:        801,
		AutoGroup: model.AutoGroupTypeNone,
		Model:     "current-model",
	}, ctx)

	remaining, err := op.GroupItemList(group.ID, ctx)
	if err != nil {
		t.Fatalf("list group items: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ModelName != "current-model" {
		t.Fatalf("应清理旧模型并保留当前模型, got %+v", remaining)
	}
}

func TestFetchModelsOpenAIUsesV1AndBearer(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}},
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIChatCompletion))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("请求路径 = %q, 期望 /v1/models", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, 期望 Bearer test-key", gotAuth)
	}
	if len(models) != 2 || models[0] != "gpt-4o" {
		t.Fatalf("解析模型 = %v, 期望 [gpt-4o gpt-4o-mini]", models)
	}
}

func TestFetchModelsDoubaoUsesV3(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{Data: []model.OpenAIModel{{ID: "doubao-pro"}}})
	}))
	defer server.Close()

	if _, err := FetchModels(context.Background(), testChannel(server.URL, model.ChannelTypeDoubao)); err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	// Doubao 复用 OpenAI 拉取逻辑，但端点版本是 v3。
	if gotPath != "/v3/models" {
		t.Fatalf("请求路径 = %q, 期望 /v3/models", gotPath)
	}
}

func TestFetchModelsGeminiUsesV1betaAndApiKeyHeader(t *testing.T) {
	var gotPath, gotKeyHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKeyHeader = r.Header.Get("X-Goog-Api-Key")
		_ = json.NewEncoder(w).Encode(model.GeminiModelList{
			Models: []model.GeminiModel{{Name: "models/gemini-1.5-pro"}},
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatGeminiContents))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if gotPath != "/v1beta/models" {
		t.Fatalf("请求路径 = %q, 期望 /v1beta/models", gotPath)
	}
	if gotKeyHeader != "test-key" {
		t.Fatalf("X-Goog-Api-Key = %q, 期望 test-key", gotKeyHeader)
	}
	// models/ 前缀应被剥离。
	if len(models) != 1 || models[0] != "gemini-1.5-pro" {
		t.Fatalf("解析模型 = %v, 期望 [gemini-1.5-pro]", models)
	}
}

func TestFetchModelsGeminiKeepsExplicitV1Suffix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(model.GeminiModelList{Models: []model.GeminiModel{{Name: "models/x"}}})
	}))
	defer server.Close()

	// 用户显式填写 /v1 时，不能再拼成 /v1/v1beta。
	if _, err := FetchModels(context.Background(), testChannel(server.URL+"/v1", llm.APIFormatGeminiContents)); err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("请求路径 = %q, 期望 /v1/models", gotPath)
	}
}

func TestFetchModelsGeminiPaginates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("pageToken") == "" {
			_ = json.NewEncoder(w).Encode(model.GeminiModelList{
				Models:        []model.GeminiModel{{Name: "models/page1"}},
				NextPageToken: "token-2",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(model.GeminiModelList{
			Models: []model.GeminiModel{{Name: "models/page2"}},
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatGeminiContents))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if calls != 2 {
		t.Fatalf("请求次数 = %d, 期望 2 次分页", calls)
	}
	if len(models) != 2 || models[0] != "page1" || models[1] != "page2" {
		t.Fatalf("解析模型 = %v, 期望 [page1 page2]", models)
	}
}

func TestFetchModelsAnthropicHeadersAndPagination(t *testing.T) {
	calls := 0
	var gotPath, gotKey, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		calls++
		if r.URL.Query().Get("after_id") == "" {
			_ = json.NewEncoder(w).Encode(model.AnthropicModelList{
				Data:    []model.AnthropicModel{{ID: "claude-3-5-sonnet"}},
				HasMore: true,
				LastID:  "claude-3-5-sonnet",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(model.AnthropicModelList{
			Data:    []model.AnthropicModel{{ID: "claude-3-opus"}},
			HasMore: false,
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatAnthropicMessage))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("请求路径 = %q, 期望 /v1/models", gotPath)
	}
	if gotKey != "test-key" {
		t.Fatalf("X-Api-Key = %q, 期望 test-key", gotKey)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q, 期望 2023-06-01", gotVersion)
	}
	if calls != 2 {
		t.Fatalf("请求次数 = %d, 期望 2 次分页", calls)
	}
	if len(models) != 2 || models[0] != "claude-3-5-sonnet" || models[1] != "claude-3-opus" {
		t.Fatalf("解析模型 = %v, 期望两页合并", models)
	}
}

func TestFetchModelsAppliesMatchRegex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "gpt-4o"}, {ID: "text-embedding-3"}, {ID: "gpt-4o-mini"}},
		})
	}))
	defer server.Close()

	channel := testChannel(server.URL, llm.APIFormatOpenAIChatCompletion)
	regex := `^gpt-`
	channel.MatchRegex = &regex

	models, err := FetchModels(context.Background(), channel)
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	// 只保留匹配 ^gpt- 的模型。
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
		t.Fatalf("过滤结果 = %v, 期望 [gpt-4o gpt-4o-mini]", models)
	}
}

func TestFetchModelsGeminiFallsBackToOpenAIWhenEmpty(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1beta/models" {
			// Gemini 返回空列表，触发回退到 OpenAI 端点。
			_ = json.NewEncoder(w).Encode(model.GeminiModelList{})
			return
		}
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{Data: []model.OpenAIModel{{ID: "fallback"}}})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatGeminiContents))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if len(models) != 1 || models[0] != "fallback" {
		t.Fatalf("回退结果 = %v, 期望 [fallback]", models)
	}
	// 第一次打 Gemini 端点，空结果后回退打 OpenAI 端点。
	if len(paths) != 2 || paths[0] != "/v1beta/models" || paths[1] != "/v1/models" {
		t.Fatalf("请求路径序列 = %v, 期望 [/v1beta/models /v1/models]", paths)
	}
}

// TestFetchModelsAnthropicFallsBackToOpenAIOnError 关键兼容性测试：
// anyrouter 等中转站对 Anthropic 类型渠道，用 X-Api-Key 请求返回 401 错误 JSON，
// 当前代码不检查状态码，直接 decode 成空列表，触发 OpenAI 回退，改用 Bearer 认证成功。
// 这是当前设计的宽松兼容路径，必须锁住防止将来误改。
func TestFetchModelsAnthropicFallsBackToOpenAIOn401(t *testing.T) {
	var paths []string
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		authHeaders = append(authHeaders, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Api-Key"))

		if r.Header.Get("X-Api-Key") != "" {
			// Anthropic 端点：anyrouter 不认 X-Api-Key，返回 401 错误 JSON
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		// OpenAI 回退端点：识别 Bearer，返回模型列表
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "gpt-4o"}, {ID: "claude-3-5-sonnet"}},
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatAnthropicMessage))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}

	// 验证：先用 X-Api-Key 失败，再用 Bearer 成功
	if len(paths) != 2 || paths[0] != "/v1/models" || paths[1] != "/v1/models" {
		t.Fatalf("请求路径序列 = %v, 期望两次 /v1/models", paths)
	}
	if authHeaders[0] != "|test-key" {
		t.Fatalf("第一次请求认证 = %q, 期望 X-Api-Key", authHeaders[0])
	}
	if authHeaders[1] != "Bearer test-key|" {
		t.Fatalf("第二次请求认证 = %q, 期望 Bearer", authHeaders[1])
	}

	// 验证：回退成功，返回 anyrouter 的模型列表
	if len(models) != 2 || models[0] != "gpt-4o" || models[1] != "claude-3-5-sonnet" {
		t.Fatalf("回退结果 = %v, 期望 anyrouter 返回的模型列表", models)
	}
}

// TestFetchModelsAnthropicFallsBackToOpenAIWhenEmpty 验证 Anthropic 返回空列表时的回退
func TestFetchModelsAnthropicFallsBackToOpenAIWhenEmpty(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Api-Key") != "" {
			// Anthropic 端点返回空列表
			_ = json.NewEncoder(w).Encode(model.AnthropicModelList{Data: []model.AnthropicModel{}})
			return
		}
		// OpenAI 回退端点
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{Data: []model.OpenAIModel{{ID: "fallback"}}})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatAnthropicMessage))
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if len(models) != 1 || models[0] != "fallback" {
		t.Fatalf("回退结果 = %v, 期望 [fallback]", models)
	}
	if len(paths) != 2 || paths[0] != "/v1/models" || paths[1] != "/v1/models" {
		t.Fatalf("请求路径序列 = %v, 期望两次 /v1/models", paths)
	}
}

func TestProbeCompactStrategyFallsBackToResponsesManual(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q, 期望 Bearer test-key", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_1"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result := ProbeCompactStrategy(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIResponse), "gpt-4o")
	if result.Strategy != model.CompactStrategyResponsesManual {
		t.Fatalf("Strategy = %q, 期望 %q, error=%s", result.Strategy, model.CompactStrategyResponsesManual, result.Error)
	}
	want := []string{"/v1/responses/compact", "/v1/responses"}
	if len(paths) != len(want) {
		t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
		}
	}
}

func TestProbeCompactStrategyFallsBackToChatManual(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/responses/compact":
			http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
		case "/v1/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid codex request (request id: x)","code":"invalid_responses_request","type":"new_api_error"}}`))
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl_1"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result := ProbeCompactStrategy(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIResponse), "gpt-4o")
	if result.Strategy != model.CompactStrategyChatManual {
		t.Fatalf("Strategy = %q, 期望 %q, error=%s", result.Strategy, model.CompactStrategyChatManual, result.Error)
	}
	want := []string{"/v1/responses/compact", "/v1/responses", "/v1/chat/completions"}
	if len(paths) != len(want) {
		t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
		}
	}
}

func TestProbeCompactStrategyMarksIncompatibleWhenAllStrategiesAreDefinitivelyUnsupported(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, `{"error":{"message":"no such endpoint"}}`, http.StatusNotFound)
	}))
	defer server.Close()

	result := ProbeCompactStrategy(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIResponse), "gpt-4o")
	if result.Strategy != model.CompactStrategyIncompatible {
		t.Fatalf("Strategy = %q, 期望 %q, error=%s", result.Strategy, model.CompactStrategyIncompatible, result.Error)
	}
	if result.Error == "" {
		t.Fatal("incompatible 结果应保留错误信息")
	}
	want := []string{"/v1/responses/compact", "/v1/responses", "/v1/chat/completions"}
	if len(paths) != len(want) {
		t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("请求路径序列 = %v, 期望 %v", paths, want)
		}
	}
}
