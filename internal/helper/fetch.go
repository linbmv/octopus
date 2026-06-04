package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/dlclark/regexp2"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

func FetchModels(ctx context.Context, request model.Channel) ([]string, error) {
	client, err := ChannelHttpClient(&request)
	if err != nil {
		return nil, err
	}

	keys := availableFetchKeys(request)
	if len(keys) == 0 {
		return nil, fmt.Errorf("没有可用的渠道密钥")
	}

	var lastErr error
	for i, key := range keys {
		requestWithKey := request
		requestWithKey.Keys = []model.ChannelKey{key}

		fetchModel, err := fetchModelsWithKey(client, ctx, requestWithKey)
		if err != nil {
			lastErr = err
			continue
		}
		// 部分中转在 key 无权列模型时会返回 200 + 空列表；继续试下一个 key，避免误判。
		if len(fetchModel) == 0 {
			lastErr = fmt.Errorf("获取模型列表失败：%s 返回空模型列表，可能无权列出模型或密钥不可用", fetchKeyLabel(key, i))
			continue
		}
		return filterFetchedModels(fetchModel, request.MatchRegex)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("获取模型列表失败：所有渠道密钥都返回空模型列表")
}

func fetchModelsWithKey(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	fetchModel := make([]string, 0)
	var err error
	switch request.Type {
	case llm.APIFormatAnthropicMessage:
		fetchModel, err = fetchAnthropicModels(client, ctx, request)
	case llm.APIFormatGeminiContents:
		fetchModel, err = fetchGeminiModels(client, ctx, request)
	default:
		fetchModel, err = fetchOpenAIModels(client, ctx, request)
	}
	if err != nil {
		return nil, err
	}
	return fetchModel, nil
}

func availableFetchKeys(request model.Channel) []model.ChannelKey {
	nowSec := time.Now().Unix()
	keys := make([]model.ChannelKey, 0, len(request.Keys))
	for _, key := range request.Keys {
		if key.IsAvailable(nowSec) {
			keys = append(keys, key)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return keys[i].TotalCost < keys[j].TotalCost
	})
	return keys
}

func filterFetchedModels(fetchModel []string, matchRegex *string) ([]string, error) {
	if matchRegex != nil && *matchRegex != "" {
		matchModel := make([]string, 0)
		re, err := regexp2.Compile(*matchRegex, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		for _, model := range fetchModel {
			matched, err := re.MatchString(model)
			if err != nil {
				return nil, err
			}
			if matched {
				matchModel = append(matchModel, model)
			}
		}
		return matchModel, nil
	}
	return fetchModel, nil
}

// refer: https://platform.openai.com/docs/api-reference/models/list
func fetchOpenAIModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	baseURL := transformer.NormalizeBaseURL(request.GetBaseUrl(), "v1")
	if request.Type == model.ChannelTypeDoubao {
		baseURL = transformer.NormalizeBaseURL(request.GetBaseUrl(), "v3")
	}
	req, _ := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		baseURL+"/models",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+request.GetChannelKey().ChannelKey)
	applyCustomHeaders(req, request)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := ensureModelResponseOK(resp, baseURL+"/models"); err != nil {
		return nil, err
	}

	var result model.OpenAIModelList

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

// refer: https://ai.google.dev/api/models
func fetchGeminiModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	var allModels []string
	pageToken := ""
	baseURL := transformer.NormalizeBaseURL(request.GetBaseUrl(), "v1beta")
	// Gemini transformer 会保留用户显式填写的 /v1；这里同样处理，避免把 /v1 拼成 /v1/v1beta。
	if strings.HasSuffix(strings.TrimRight(request.GetBaseUrl(), "/"), "/v1") {
		baseURL = transformer.NormalizeBaseURL(request.GetBaseUrl(), "")
	}

	for {
		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/models",
			nil,
		)
		req.Header.Set("X-Goog-Api-Key", request.GetChannelKey().ChannelKey)
		applyCustomHeaders(req, request)
		if pageToken != "" {
			q := req.URL.Query()
			q.Add("pageToken", pageToken)
			req.URL.RawQuery = q.Encode()
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if err := ensureModelResponseOK(resp, baseURL+"/models"); err != nil {
			return nil, err
		}

		var result model.GeminiModelList

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}

		for _, m := range result.Models {
			name := strings.TrimPrefix(m.Name, "models/")
			allModels = append(allModels, name)
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request)
	}
	return allModels, nil
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {

	var allModels []string
	var afterID string
	baseURL := transformer.NormalizeBaseURL(request.GetBaseUrl(), "v1")
	for {

		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/models",
			nil,
		)
		req.Header.Set("X-Api-Key", request.GetChannelKey().ChannelKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		applyCustomHeaders(req, request)
		// 设置多页参数
		q := req.URL.Query()

		if afterID != "" {
			q.Set("after_id", afterID)
		}
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if err := ensureModelResponseOK(resp, baseURL+"/models"); err != nil {
			return nil, err
		}

		var result model.AnthropicModelList

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}

		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}

		if !result.HasMore {
			break
		}

		afterID = result.LastID
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request)
	}
	return allModels, nil
}

func ensureModelResponseOK(resp *http.Response, endpoint string) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		// 2xx 但 Content-Type 是 HTML：通常是 Cloudflare 人机校验页、网络劫持，或 Base URL 指向了网页而非 API。
		// 直接 json 解码只会得到隐晦的 "invalid character '<'"，这里转成可诊断的明确错误。
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
			htmlBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return fmt.Errorf("模型列表接口 %s 返回了 HTML 而非 JSON（Content-Type=%q），常见于部署机访问该站点被 Cloudflare 人机校验/网络劫持拦截，或 Base URL 配置错误。HTML 片段：%s",
				endpoint, resp.Header.Get("Content-Type"), truncateText(strings.TrimSpace(string(htmlBody)), 120))
		}
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := extractUpstreamErrorMessage(body)
	if message != "" {
		return fmt.Errorf("模型列表接口请求失败：%s 返回 HTTP %d，%s", endpoint, resp.StatusCode, message)
	}

	bodyText := strings.TrimSpace(string(body))
	if bodyText != "" {
		return fmt.Errorf("模型列表接口请求失败：%s 返回 HTTP %d，响应：%s", endpoint, resp.StatusCode, truncateText(bodyText, 300))
	}
	return fmt.Errorf("模型列表接口请求失败：%s 返回 HTTP %d", endpoint, resp.StatusCode)
}

func extractUpstreamErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	errorValue, ok := payload["error"]
	if !ok {
		return ""
	}
	if message, ok := errorValue.(string); ok {
		return strings.TrimSpace(message)
	}
	if errorMap, ok := errorValue.(map[string]any); ok {
		if message, ok := errorMap["message"].(string); ok {
			return strings.TrimSpace(message)
		}
	}
	return ""
}

func fetchKeyLabel(key model.ChannelKey, index int) string {
	if key.ID > 0 {
		return fmt.Sprintf("密钥 ID %d", key.ID)
	}
	return fmt.Sprintf("第 %d 个密钥", index+1)
}

func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

// defaultFetchUserAgent 给模型拉取请求一个浏览器风格 UA，避免被部分 CDN/WAF（如 Cloudflare bot 检测）
// 按默认 "Go-http-client/1.1" 判为爬虫而返回人机校验页。用户可用 CustomHeader 里的 User-Agent 覆盖。
// 注意：若上游基于 TLS 指纹（JA3/JA4）识别客户端，仅改 UA 不足以绕过，需改用代理出口。
const defaultFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func applyCustomHeaders(req *http.Request, channel model.Channel) {
	// 默认补浏览器风格 UA/Accept；放在自定义头之前，用户在 CustomHeader 里配置的同名头可覆盖。
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultFetchUserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	for _, header := range channel.CustomHeader {
		if header.HeaderKey != "" {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}
