package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/bestruirui/octopus/internal/codexauth"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

const (
	maxModelListPages         = 100
	maxModelPageTokenSize     = 4096
	maxModelListResponseBytes = 64 << 20
	maxFetchedModelCount      = 50_000
)

var errModelListResourceLimit = errors.New("model discovery resource limit exceeded")

type modelFetchBudget struct {
	mu        sync.Mutex
	remaining int64
	max       int64
}

func newModelFetchBudget(maxBytes int64) *modelFetchBudget {
	return &modelFetchBudget{remaining: maxBytes, max: maxBytes}
}

func (b *modelFetchBudget) readResponse(response *http.Response, provider string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remaining <= 0 {
		return nil, fmt.Errorf("%w: cumulative %s response bytes exceeded %d", errModelListResourceLimit, provider, b.max)
	}
	pageLimit := min(b.remaining, bodylimit.DefaultMetadataResponseBytes)
	body, err := bodylimit.ReadResponseBody(response, pageLimit)
	if err != nil {
		if errors.Is(err, bodylimit.ErrTooLarge) {
			return nil, fmt.Errorf("%w: %s response page or cumulative bytes exceed limit: %w", errModelListResourceLimit, provider, err)
		}
		return nil, err
	}
	b.remaining -= int64(len(body))
	return body, nil
}

type modelAccumulator struct {
	models []string
	seen   map[string]struct{}
	max    int
}

func newModelAccumulator(maxModels int) *modelAccumulator {
	return &modelAccumulator{seen: make(map[string]struct{}), max: maxModels}
}

func (a *modelAccumulator) add(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if _, exists := a.seen[name]; exists {
		return nil
	}
	if len(a.models) >= a.max {
		return fmt.Errorf("%w: unique model count exceeds %d", errModelListResourceLimit, a.max)
	}
	a.seen[name] = struct{}{}
	a.models = append(a.models, name)
	return nil
}

type fetchResult struct {
	models []string
	err    error
}

func FetchModels(ctx context.Context, request model.Channel) ([]string, error) {
	if request.Type == model.ChannelTypeOpenAICodex {
		planType := ""
		bestRank := -1
		for _, key := range request.Keys {
			if !key.Enabled {
				continue
			}
			document, err := codexauth.Parse(key.ChannelKey)
			if err != nil {
				continue
			}
			if rank := codexPlanRank(document.PlanType()); rank > bestRank {
				bestRank = rank
				planType = document.PlanType()
			}
		}
		models := codexModelsForPlan(planType)
		if request.MatchRegex != nil && *request.MatchRegex != "" {
			return filterModels(models, *request.MatchRegex)
		}
		return models, nil
	}
	client, err := ChannelHttpClient(&request)
	if err != nil {
		return nil, err
	}

	keys := request.AvailableKeys()
	if len(keys) == 0 {
		return nil, fmt.Errorf("channel %s has no available API key", request.Name)
	}

	results := make([]fetchResult, len(keys))
	budget := newModelFetchBudget(maxModelListResponseBytes)
	var wg sync.WaitGroup
	for i, key := range keys {
		i, key := i, key
		wg.Add(1)
		go func() {
			defer wg.Done()
			models, fetchErr := fetchModelsWithKeyBudget(client, ctx, request, key.ChannelKey, budget)
			results[i] = fetchResult{models: models, err: fetchErr}
		}()
	}
	wg.Wait()

	fetchModel, firstErr := mergeFetchedModels(results)
	if errors.Is(firstErr, errModelListResourceLimit) {
		return nil, firstErr
	}
	if len(fetchModel) == 0 && firstErr != nil {
		return nil, firstErr
	}
	if request.MatchRegex != nil && *request.MatchRegex != "" {
		return filterModels(fetchModel, *request.MatchRegex)
	}
	return fetchModel, nil
}

func codexPlanRank(plan string) int {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro":
		return 7
	case "team", "business", "enterprise", "edu", "education":
		return 6
	case "plus", "go", "prolite":
		return 5
	case "free", "free_workspace", "k12":
		return 1
	default:
		return 0
	}
}

func mergeFetchedModels(results []fetchResult) ([]string, error) {
	accumulator := newModelAccumulator(maxFetchedModelCount)
	var firstErr error
	for _, result := range results {
		if result.err != nil {
			if errors.Is(result.err, errModelListResourceLimit) {
				return nil, result.err
			}
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		for _, name := range result.models {
			if err := accumulator.add(name); err != nil {
				return nil, err
			}
		}
	}
	return accumulator.models, firstErr
}

func filterModels(models []string, pattern string) ([]string, error) {
	re, err := CompileModelRegex(pattern)
	if err != nil {
		return nil, err
	}
	matchedModels := make([]string, 0, len(models))
	for _, name := range models {
		matched, err := MatchModelRegex(re, name)
		if err != nil {
			return nil, err
		}
		if matched {
			matchedModels = append(matchedModels, name)
		}
	}
	return matchedModels, nil
}

func fetchModelsWithKey(client *http.Client, ctx context.Context, request model.Channel, key string) ([]string, error) {
	return fetchModelsWithKeyBudget(client, ctx, request, key, newModelFetchBudget(maxModelListResponseBytes))
}

func fetchModelsWithKeyBudget(client *http.Client, ctx context.Context, request model.Channel, key string, budget *modelFetchBudget) ([]string, error) {
	switch request.Type {
	case llm.APIFormatAnthropicMessage:
		models, err := fetchAnthropicModelsWithBudget(client, ctx, request, key, budget)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if errors.Is(err, errModelListResourceLimit) {
			return nil, err
		}
		fallback, fallbackErr := fetchOpenAIModelsWithBudget(client, ctx, request, key, budget)
		if errors.Is(fallbackErr, errModelListResourceLimit) {
			return nil, fallbackErr
		}
		if fallbackErr == nil && len(fallback) > 0 {
			return fallback, nil
		}
		if err != nil {
			return models, err
		}
		if fallbackErr == nil {
			return fallback, nil
		}
		return models, nil
	case llm.APIFormatGeminiContents:
		models, err := fetchGeminiModelsWithBudget(client, ctx, request, key, budget)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if errors.Is(err, errModelListResourceLimit) {
			return nil, err
		}
		fallback, fallbackErr := fetchOpenAIModelsWithBudget(client, ctx, request, key, budget)
		if errors.Is(fallbackErr, errModelListResourceLimit) {
			return nil, fallbackErr
		}
		if fallbackErr == nil && len(fallback) > 0 {
			return fallback, nil
		}
		if err != nil {
			return models, err
		}
		if fallbackErr == nil {
			return fallback, nil
		}
		return models, nil
	default:
		return fetchOpenAIModelsWithBudget(client, ctx, request, key, budget)
	}
}

// refer: https://platform.openai.com/docs/api-reference/models/list
func fetchOpenAIModelsWithBudget(client *http.Client, ctx context.Context, request model.Channel, key string, budget *modelFetchBudget) ([]string, error) {
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
	req.Header.Set("Authorization", "Bearer "+key)
	applyCustomHeaders(req, request)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	var result model.OpenAIModelList
	if err := decodeModelListPage(resp, "OpenAI", &result, budget); err != nil {
		return nil, err
	}

	models := newModelAccumulator(maxFetchedModelCount)
	for _, m := range result.Data {
		if err := models.add(m.ID); err != nil {
			return nil, err
		}
	}
	return models.models, nil
}

// refer: https://ai.google.dev/api/models
func fetchGeminiModels(client *http.Client, ctx context.Context, request model.Channel, key string) ([]string, error) {
	return fetchGeminiModelsWithBudget(client, ctx, request, key, newModelFetchBudget(maxModelListResponseBytes))
}

func fetchGeminiModelsWithBudget(client *http.Client, ctx context.Context, request model.Channel, key string, budget *modelFetchBudget) ([]string, error) {
	allModels := newModelAccumulator(maxFetchedModelCount)
	pageToken := ""
	seenTokens := make(map[string]struct{})
	baseURL := transformer.NormalizeBaseURL(request.GetBaseUrl(), "v1beta")
	// Gemini transformer 会保留用户显式填写的 /v1；这里同样处理，避免把 /v1 拼成 /v1/v1beta。
	if strings.HasSuffix(strings.TrimRight(request.GetBaseUrl(), "/"), "/v1") {
		baseURL = transformer.NormalizeBaseURL(request.GetBaseUrl(), "")
	}

	for page := 1; page <= maxModelListPages; page++ {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/models",
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("build Gemini models request: %w", err)
		}
		req.Header.Set("X-Goog-Api-Key", key)
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

		var result model.GeminiModelList
		if err := decodeModelListPage(resp, "Gemini", &result, budget); err != nil {
			return nil, err
		}

		for _, m := range result.Models {
			name := strings.TrimPrefix(m.Name, "models/")
			if err := allModels.add(name); err != nil {
				return nil, err
			}
		}

		if result.NextPageToken == "" {
			return allModels.models, nil
		}
		nextToken, err := validateContinuationToken("Gemini", result.NextPageToken, seenTokens)
		if err != nil {
			return nil, err
		}
		if page == maxModelListPages {
			return nil, fmt.Errorf("gemini model pagination exceeded %d pages", maxModelListPages)
		}
		pageToken = nextToken
	}
	return nil, fmt.Errorf("gemini model pagination exceeded %d pages", maxModelListPages)
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(client *http.Client, ctx context.Context, request model.Channel, key string) ([]string, error) {
	return fetchAnthropicModelsWithBudget(client, ctx, request, key, newModelFetchBudget(maxModelListResponseBytes))
}

func fetchAnthropicModelsWithBudget(client *http.Client, ctx context.Context, request model.Channel, key string, budget *modelFetchBudget) ([]string, error) {
	allModels := newModelAccumulator(maxFetchedModelCount)
	var afterID string
	seenTokens := make(map[string]struct{})
	baseURL := transformer.NormalizeBaseURL(request.GetBaseUrl(), "v1")
	for page := 1; page <= maxModelListPages; page++ {

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			baseURL+"/models",
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("build Anthropic models request: %w", err)
		}
		req.Header.Set("X-Api-Key", key)
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

		var result model.AnthropicModelList
		if err := decodeModelListPage(resp, "Anthropic", &result, budget); err != nil {
			return nil, err
		}

		for _, m := range result.Data {
			if err := allModels.add(m.ID); err != nil {
				return nil, err
			}
		}

		if !result.HasMore {
			return allModels.models, nil
		}
		nextToken, err := validateContinuationToken("Anthropic", result.LastID, seenTokens)
		if err != nil {
			return nil, err
		}
		if page == maxModelListPages {
			return nil, fmt.Errorf("anthropic model pagination exceeded %d pages", maxModelListPages)
		}
		afterID = nextToken
	}
	return nil, fmt.Errorf("anthropic model pagination exceeded %d pages", maxModelListPages)
}

func decodeModelListPage(resp *http.Response, provider string, target any, budget *modelFetchBudget) (err error) {
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s models response body: %w", provider, closeErr))
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch %s models failed: status %d", provider, resp.StatusCode)
	}
	if budget == nil {
		budget = newModelFetchBudget(maxModelListResponseBytes)
	}
	body, err := budget.readResponse(resp, provider)
	if err != nil {
		return fmt.Errorf("read %s models response: %w", provider, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode %s models response: %w", provider, err)
	}
	return nil
}

func validateContinuationToken(provider, rawToken string, seen map[string]struct{}) (string, error) {
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return "", fmt.Errorf("%s model pagination returned an empty continuation token", provider)
	}
	if len(token) > maxModelPageTokenSize {
		return "", fmt.Errorf("%s model pagination token exceeds %d bytes", provider, maxModelPageTokenSize)
	}
	if _, exists := seen[token]; exists {
		return "", fmt.Errorf("%s model pagination repeated continuation token", provider)
	}
	seen[token] = struct{}{}
	return token, nil
}

func applyCustomHeaders(req *http.Request, channel model.Channel) {
	for _, header := range channel.CustomHeader {
		if header.HeaderKey != "" && !requestrewrite.IsProtectedHeader(header.HeaderKey) {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}
