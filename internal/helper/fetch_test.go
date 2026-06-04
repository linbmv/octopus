package helper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
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

func TestFetchModelsSkipsFailedKeyAndUsesNext(t *testing.T) {
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		auths = append(auths, auth)
		if auth == "Bearer bad-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "Invalid token"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "claude-opus-4-8"}, {ID: "gpt-5.5"}},
		})
	}))
	defer server.Close()

	channel := testChannel(server.URL, llm.APIFormatOpenAIChatCompletion)
	channel.Keys = []model.ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "bad-key", TotalCost: 0},
		{ID: 2, Enabled: true, ChannelKey: "good-key", TotalCost: 1},
	}

	models, err := FetchModels(context.Background(), channel)
	if err != nil {
		t.Fatalf("FetchModels 错误: %v", err)
	}
	if len(models) != 2 || models[0] != "claude-opus-4-8" || models[1] != "gpt-5.5" {
		t.Fatalf("模型列表 = %v，期望 [claude-opus-4-8 gpt-5.5]", models)
	}
	if len(auths) != 2 || auths[0] != "Bearer bad-key" || auths[1] != "Bearer good-key" {
		t.Fatalf("Authorization 序列 = %v，期望先尝试坏 key 再尝试好 key", auths)
	}
}

func TestFetchModelsReturnsHTTPErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "Invalid token"},
		})
	}))
	defer server.Close()

	_, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIChatCompletion))
	if err == nil {
		t.Fatal("FetchModels 未返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP 401") || !strings.Contains(msg, "Invalid token") {
		t.Fatalf("错误信息 = %q，期望包含 HTTP 401 和 Invalid token", msg)
	}
}

func TestFetchModelsReturnsErrorOnHTMLResponse(t *testing.T) {
	// 模拟 Cloudflare 人机校验页：HTTP 200 + text/html。
	// 期望被识别为 HTML 并给出可诊断错误，而不是隐晦的 "invalid character '<'"。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Just a moment...</title></head><body></body></html>"))
	}))
	defer server.Close()

	_, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatOpenAIChatCompletion))
	if err == nil {
		t.Fatal("FetchModels 未返回错误（期望识别 HTML 响应）")
	}
	if !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("错误信息 = %q，期望包含 HTML 提示", err.Error())
	}
}

func TestFetchModelsAnthropicFallsBackToOpenAIOnHTTPError(t *testing.T) {
	// anyrouter 类双格式网关：/v1/models 用 x-api-key 返回 401，但用 Bearer(OpenAI 风格)能列模型。
	// Anthropic 类型渠道应在 x-api-key 失败后回退到 Bearer，仍拿到模型，而不是直接报 401。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "未提供令牌"}})
			return
		}
		// Bearer 回退路径
		_ = json.NewEncoder(w).Encode(model.OpenAIModelList{
			Data: []model.OpenAIModel{{ID: "claude-opus-4-8"}, {ID: "claude-sonnet-4-5-20250929"}},
		})
	}))
	defer server.Close()

	models, err := FetchModels(context.Background(), testChannel(server.URL, llm.APIFormatAnthropicMessage))
	if err != nil {
		t.Fatalf("期望回退到 OpenAI 成功，却报错: %v", err)
	}
	if len(models) != 2 || models[0] != "claude-opus-4-8" {
		t.Fatalf("models=%v，期望回退拿到 [claude-opus-4-8 claude-sonnet-4-5-20250929]", models)
	}
}
