package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/doubao"
	"github.com/looplj/axonhub/llm/transformer/gemini"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/sjson"
)

// selectChannelEndpoint resolves the logical channel into one concrete URL and
// credential for a request attempt. The first attempt follows stored order;
// subsequent attempts rotate independently through enabled, non-cooled keys
// and URLs. This keeps the C06 single-value relay API intact while making the
// restored Edge multi-value configuration operational.
//
// The second return value is the selected ChannelKey ID, or 0 when the channel
// carries no separate credentials and the legacy Channel.Key is used. Callers
// report per-credential outcomes against that ID so a rate-limited key can be
// cooled down independently of its channel.
func selectChannelEndpoint(channel model.Channel, attempt int) (model.Channel, int, error) {
	if attempt < 0 {
		attempt = 0
	}
	selected := channel
	urls := append([]model.BaseUrl(nil), channel.BaseUrls...)
	if len(urls) == 0 && strings.TrimSpace(channel.BaseURL) != "" {
		urls = []model.BaseUrl{{URL: channel.BaseURL}}
	}
	if len(urls) > 0 {
		sort.SliceStable(urls, func(i, j int) bool {
			if urls[i].Delay == urls[j].Delay {
				return i < j
			}
			return urls[i].Delay < urls[j].Delay
		})
		selected.BaseURL = urls[attempt%len(urls)].URL
	}

	if len(channel.Keys) == 0 {
		return selected, 0, nil
	}
	now := time.Now().Unix()
	keys := make([]model.ChannelKey, 0, len(channel.Keys))
	for _, key := range channel.Keys {
		if key.IsAvailable(now) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return model.Channel{}, 0, fmt.Errorf("channel %q has no available credentials", channel.Name)
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	chosen := keys[attempt%len(keys)]
	selected.Key = chosen.ChannelKey
	return selected, chosen.ID, nil
}

// selectChannelEndpointForModel walks the available credentials when the
// first one is circuit-open. URL rotation remains independent, while the
// breaker is intentionally keyed at channel+credential+model rather than URL.
// A half-open probe lease is returned to the caller and must be settled after
// the request reaches a verdict, or aborted when the request never reaches the
// upstream.
func selectChannelEndpointForModel(channel model.Channel, attempt int, modelName string) (model.Channel, int, circuitPermit, error) {
	availableKeys := 1
	if len(channel.Keys) > 0 {
		availableKeys = 0
		now := time.Now().Unix()
		for _, key := range channel.Keys {
			if key.IsAvailable(now) {
				availableKeys++
			}
		}
		if availableKeys == 0 {
			selected, keyID, err := selectChannelEndpoint(channel, attempt)
			return selected, keyID, circuitPermit{}, err
		}
	}

	var earliest time.Duration
	for offset := 0; offset < availableKeys; offset++ {
		selected, keyID, err := selectChannelEndpoint(channel, attempt+offset)
		if err != nil {
			return model.Channel{}, 0, circuitPermit{}, err
		}
		allowed, permit, remaining := tryCircuit(selected.ID, keyID, modelName)
		if allowed {
			return selected, keyID, permit, nil
		}
		if remaining > 0 && (earliest == 0 || remaining < earliest) {
			earliest = remaining
		}
	}
	return model.Channel{}, 0, circuitPermit{}, &circuitUnavailableError{
		channelID: channel.ID,
		model:     modelName,
		remaining: earliest,
	}
}

// upstreamFailureStatus 提取上游失败的 HTTP 状态码和 Retry-After 秒数。
// 网络层失败没有状态码, 此时返回 0, 调用方只更新最近使用时间而不冷却凭据。
func upstreamFailureStatus(err error, now time.Time) (int, int64) {
	var failure *httpclient.Error
	if !errors.As(err, &failure) {
		return 0, 0
	}
	return failure.StatusCode, parseRetryAfterSeconds(failure.Headers, now)
}

// classifyUpstreamFailure 判定一次上游失败的错误级别, 供重试与冷却决策使用。
// 网络层失败没有状态码和响应体, 归为渠道级: 换一个成员比在同一渠道上重试更可能成功。
// 客户端级错误 (例如请求体本身非法) 不冷却任何成员, 由调用方立即终止请求,
// 否则同一个 400 会在每个成员上重放并把整组渠道拖入冷却。
func classifyUpstreamFailure(err error) errorclass.Classification {
	var failure *httpclient.Error
	if !errors.As(err, &failure) {
		return errorclass.Classification{Level: errorclass.ErrorLevelChannel, Reason: "upstream transport failure"}
	}
	return errorclass.ClassifyWithHeaders(failure.StatusCode, failure.Headers, failure.Body)
}

// parseRetryAfterSeconds 解析 Retry-After 头, 支持秒数和 HTTP 日期两种形式。
// 无法解析或已过期时返回 0, 由 op 层套用缺省冷却。
func parseRetryAfterSeconds(header http.Header, now time.Time) int64 {
	if header == nil {
		return 0
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		return seconds
	}
	deadline, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	if remaining := int64(deadline.Sub(now).Seconds()); remaining > 0 {
		return remaining
	}
	return 0
}

// buildOutbound 按渠道协议构造出站转换器, 并判断客户端请求能否直接透传。
func buildOutbound(channel model.Channel, format llm.APIFormat) (transformer.Outbound, bool, error) {
	key := auth.NewStaticKeyProvider(channel.Key)
	switch channel.Type {
	case model.ChannelProviderOpenAI:
		outbound, err := openai.NewOutboundTransformerWithConfig(&openai.Config{PlatformType: openai.PlatformOpenAI, BaseURL: channel.BaseURL, APIKeyProvider: key})
		return outbound, format == llm.APIFormatOpenAIChatCompletion, err
	case model.ChannelProviderOpenAIResponses:
		outbound, err := responses.NewOutboundTransformerWithConfig(&responses.Config{BaseURL: channel.BaseURL, APIKeyProvider: key})
		return outbound, format == llm.APIFormatOpenAIResponse, err
	case model.ChannelProviderAnthropic:
		outbound, err := anthropic.NewOutboundTransformerWithConfig(&anthropic.Config{Type: anthropic.PlatformDirect, BaseURL: channel.BaseURL, APIKeyProvider: key})
		return outbound, format == llm.APIFormatAnthropicMessage, err
	case model.ChannelProviderGemini:
		outbound, err := gemini.NewOutboundTransformerWithConfig(gemini.Config{BaseURL: channel.BaseURL, APIKeyProvider: key})
		return outbound, false, err
	case model.ChannelProviderVolcengine:
		outbound, err := doubao.NewOutboundTransformerWithConfig(&doubao.Config{BaseURL: channel.BaseURL, APIKeyProvider: key})
		return outbound, false, err
	default:
		return nil, false, fmt.Errorf("unsupported channel provider: %s", channel.Type)
	}
}

// applyChannelConfig 按渠道配置覆盖上游请求的参数并追加自定义 Header; model 与 stream 由转发流程决定, 不允许覆盖。
func applyChannelConfig(channel model.Channel, request *httpclient.Request) error {
	if channel.ParamOverride != nil && *channel.ParamOverride != "" {
		var overrides map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*channel.ParamOverride), &overrides); err != nil {
			return fmt.Errorf("invalid channel parameter override: %w", err)
		}
		body := request.Body
		// 覆盖键可能自带点号或冒号, 转义后再作为 sjson 路径使用, 避免被解析成嵌套路径。
		escape := strings.NewReplacer("\\", "\\\\", ".", "\\.", ":", "\\:")
		for key, value := range overrides {
			if key == "model" || key == "stream" {
				continue
			}
			next, err := sjson.SetRawBytes(body, ":"+escape.Replace(key), value)
			if err != nil {
				return fmt.Errorf("apply channel parameter %q: %w", key, err)
			}
			body = next
		}
		request.Body = body
		if len(request.JSONBody) > 0 {
			request.JSONBody = slices.Clone(body)
		}
	}

	// JSON 改写在 ParamOverride 之后生效, 让精确的 Pointer 规则可以修正顶层覆盖的结果。
	// 只处理 JSON 请求体; multipart 等请求跳过, 避免把二进制体当作文档解析。
	if len(channel.JSONRewriteRules) > 0 && isJSONRequest(request) {
		body, changed, err := applyJSONRewriteRules(request.Body, channel.JSONRewriteRules)
		if err != nil {
			return err
		}
		if changed {
			request.Body = body
			if len(request.JSONBody) > 0 {
				request.JSONBody = slices.Clone(body)
			}
		}
	}

	// 转换器已经写入的认证等敏感 Header 不允许被自定义配置覆盖。
	for _, header := range channel.CustomHeader {
		if isProtectedHeader(header.HeaderKey) {
			continue
		}
		request.Headers.Set(header.HeaderKey, header.HeaderValue)
	}

	// HeaderRules 在 CustomHeader 之后按序生效, 补充 append 与 remove 语义。
	for _, rule := range channel.HeaderRules {
		if isProtectedHeader(rule.HeaderKey) {
			continue
		}
		key := strings.TrimSpace(rule.HeaderKey)
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "set":
			request.Headers.Set(key, rule.HeaderValue)
		case "append":
			request.Headers.Add(key, rule.HeaderValue)
		case "remove":
			request.Headers.Del(key)
		}
	}
	return nil
}

// isProtectedHeader 判断 Header 是否可能携带上游凭据。认证由协议转换器负责,
// 渠道改写配置一律不得触碰这些 Header。上游 httpclient.IsSensitiveHeader 只做
// 固定表精确匹配, 这里补齐 Edge 覆盖的前后缀规则(如 *-token、chatgpt-account-id)。
func isProtectedHeader(name string) bool {
	return httpclient.IsSensitiveHeader(name) || requestrewrite.IsProtectedHeader(name)
}

// isJSONRequest 判断出站请求体是否为 JSON, 决定能否按文档改写。
func isJSONRequest(request *httpclient.Request) bool {
	probe := strings.ToLower(request.Headers.Get("Content-Type") + " " + request.ContentType)
	return strings.Contains(probe, "application/json")
}

// applyJSONRewriteRules 按顺序套用 JSON Pointer 改写规则。规则命中不存在的路径
// 是安全的空操作; 只有全部规则都未命中时返回 changed=false, 保持原始字节不变。
func applyJSONRewriteRules(body []byte, rules []model.JSONRewriteRule) ([]byte, bool, error) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return body, false, fmt.Errorf("decode request body: %w", err)
	}
	changed := false
	for i, rule := range rules {
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		tokens, err := requestrewrite.ParseJSONPointer(strings.TrimSpace(rule.Path))
		if err != nil {
			return body, false, fmt.Errorf("json rewrite rule %d path: %w", i, err)
		}
		var value any
		if action == "override" {
			if rule.Value == nil {
				return body, false, fmt.Errorf("json rewrite rule %d override value is required", i)
			}
			if err := json.Unmarshal([]byte(*rule.Value), &value); err != nil {
				return body, false, fmt.Errorf("json rewrite rule %d value: %w", i, err)
			}
		}
		updated, applied, err := requestrewrite.ApplyJSONPointer(document, tokens, action, value)
		if err != nil {
			return body, false, fmt.Errorf("json rewrite rule %d: %w", i, err)
		}
		if applied {
			document = updated
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	modified, err := json.Marshal(document)
	if err != nil {
		return body, false, fmt.Errorf("encode rewritten request body: %w", err)
	}
	return modified, true, nil
}
