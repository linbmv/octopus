package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

const maxProbeResponseBytes = 1 << 20

const probePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

type ProbeResult struct {
	Status       model.CapabilityStatus
	ErrorClass   string
	ErrorLevel   string
	ErrorMessage string
	HTTPStatus   int
}

type Prober interface {
	Probe(context.Context, *model.Channel, model.ChannelKey, string, string, model.Capability, int) ProbeResult
}

type HTTPProber struct {
	ClientForChannel func(*model.Channel) (*http.Client, error)
}

func (p HTTPProber) Probe(
	ctx context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint string,
	modelName string,
	capability model.Capability,
	maxOutputTokens int,
) ProbeResult {
	request, err := buildProbeRequest(ctx, channel, key, endpoint, modelName, capability, maxOutputTokens)
	if err != nil {
		return ProbeResult{
			Status:       model.CapabilityNotImplemented,
			ErrorClass:   "wire_protocol",
			ErrorMessage: safeProbeMessage(err.Error()),
		}
	}
	clientForChannel := p.ClientForChannel
	if clientForChannel == nil {
		clientForChannel = helper.ChannelHttpClient
	}
	client, err := clientForChannel(channel)
	if err != nil {
		return transientProbeResult("client_configuration", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return transientProbeResult(classifyTransportError(err), err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxProbeResponseBytes+1))
	if readErr != nil {
		return transientProbeResult("response_read", readErr)
	}
	if len(body) > maxProbeResponseBytes {
		return ProbeResult{
			Status:       model.CapabilityTransient,
			ErrorClass:   "response_too_large",
			ErrorLevel:   errorclass.ErrorLevelChannel.String(),
			ErrorMessage: "probe response exceeded the bounded read limit",
			HTTPStatus:   response.StatusCode,
		}
	}

	classification := errorclass.ClassifyWithHeaders(response.StatusCode, response.Header, body)
	if response.StatusCode >= 200 && response.StatusCode < 300 && classification.Level == errorclass.ErrorLevelNone {
		switch capability {
		case model.CapabilityTool:
			if !responseContainsToolCall(body) {
				return ProbeResult{
					Status:       model.CapabilityNotImplemented,
					ErrorClass:   "missing_tool_call",
					ErrorMessage: "provider accepted tools but did not return the forced probe call",
					HTTPStatus:   response.StatusCode,
				}
			}
		case model.CapabilityStream:
			contentType := strings.ToLower(response.Header.Get("Content-Type"))
			if !strings.Contains(contentType, "text/event-stream") && !bytes.Contains(body, []byte("data:")) {
				return ProbeResult{
					Status:       model.CapabilityUnsupported,
					ErrorClass:   "non_stream_response",
					ErrorMessage: "provider ignored the streaming request",
					HTTPStatus:   response.StatusCode,
				}
			}
		}
		return ProbeResult{Status: model.CapabilitySupported, HTTPStatus: response.StatusCode}
	}

	message := extractProviderError(body)
	status, class := classifyProbeFailure(response.StatusCode, message, capability, classification)
	if message == "provider rejected the capability probe" && classification.Reason != "" {
		message = classification.Reason
	}
	return ProbeResult{
		Status:       status,
		ErrorClass:   class,
		ErrorLevel:   classification.Level.String(),
		ErrorMessage: safeProbeMessage(message),
		HTTPStatus:   response.StatusCode,
	}
}

func buildProbeRequest(
	ctx context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint string,
	modelName string,
	capability model.Capability,
	maxOutputTokens int,
) (*http.Request, error) {
	if channel == nil || key.ChannelKey == "" || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(modelName) == "" {
		return nil, errors.New("incomplete probe scope")
	}
	if !capability.Valid() {
		return nil, errors.New("unsupported probe capability")
	}
	if maxOutputTokens <= 0 {
		return nil, errors.New("probe output token limit must be positive")
	}
	var target string
	var payload map[string]any
	switch channel.Type {
	case llm.APIFormatOpenAIChatCompletion, model.ChannelTypeDoubao:
		version := "v1"
		if channel.Type == model.ChannelTypeDoubao {
			version = "v3"
		}
		target = transformer.NormalizeBaseURL(endpoint, version) + "/chat/completions"
		payload = openAIChatProbePayload(modelName, capability, maxOutputTokens)
	case llm.APIFormatOpenAIResponse:
		target = transformer.NormalizeBaseURL(endpoint, "v1") + "/responses"
		payload = openAIResponsesProbePayload(modelName, capability, maxOutputTokens)
	case llm.APIFormatAnthropicMessage:
		target = transformer.NormalizeBaseURL(endpoint, "v1") + "/messages"
		payload = anthropicProbePayload(modelName, capability, maxOutputTokens)
	case llm.APIFormatGeminiContents:
		method := "generateContent"
		if capability == model.CapabilityStream {
			method = "streamGenerateContent"
		}
		geminiModel := strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
		target = transformer.NormalizeBaseURL(endpoint, "v1beta") + "/models/" + url.PathEscape(geminiModel) + ":" + method
		if capability == model.CapabilityStream {
			target += "?alt=sse"
		}
		payload = geminiProbePayload(capability, maxOutputTokens)
	default:
		return nil, fmt.Errorf("wire protocol %s has no chat capability probe", channel.Type)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode probe request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	switch channel.Type {
	case llm.APIFormatAnthropicMessage:
		request.Header.Set("x-api-key", key.ChannelKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	case llm.APIFormatGeminiContents:
		request.Header.Set("x-goog-api-key", key.ChannelKey)
	default:
		request.Header.Set("Authorization", "Bearer "+key.ChannelKey)
	}
	applyChannelProbeHeaders(request, channel)
	return request, nil
}

func openAIChatProbePayload(modelName string, capability model.Capability, maxOutputTokens int) map[string]any {
	payload := map[string]any{
		"model":      modelName,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with OK."}},
		"max_tokens": maxOutputTokens,
		"stream":     capability == model.CapabilityStream,
	}
	switch capability {
	case model.CapabilityTool:
		payload["messages"] = []any{map[string]any{"role": "user", "content": "Call octopus_capability_probe now."}}
		payload["tools"] = []any{openAIFunctionTool()}
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "octopus_capability_probe"}}
	case model.CapabilityVision:
		payload["messages"] = []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Reply with OK."},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + probePNGBase64, "detail": "low"}},
			},
		}}
	}
	return payload
}

func openAIResponsesProbePayload(modelName string, capability model.Capability, maxOutputTokens int) map[string]any {
	payload := map[string]any{
		"model":             modelName,
		"input":             "Reply with OK.",
		"max_output_tokens": maxOutputTokens,
		"stream":            capability == model.CapabilityStream,
	}
	switch capability {
	case model.CapabilityTool:
		payload["input"] = "Call octopus_capability_probe now."
		payload["tools"] = []any{map[string]any{
			"type": "function", "name": "octopus_capability_probe",
			"description": "Return a successful capability probe.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}}
		payload["tool_choice"] = map[string]any{"type": "function", "name": "octopus_capability_probe"}
	case model.CapabilityVision:
		payload["input"] = []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "Reply with OK."},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + probePNGBase64, "detail": "low"},
			},
		}}
	}
	return payload
}

func anthropicProbePayload(modelName string, capability model.Capability, maxOutputTokens int) map[string]any {
	payload := map[string]any{
		"model":      modelName,
		"max_tokens": maxOutputTokens,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with OK."}},
		"stream":     capability == model.CapabilityStream,
	}
	switch capability {
	case model.CapabilityTool:
		payload["messages"] = []any{map[string]any{"role": "user", "content": "Call octopus_capability_probe now."}}
		payload["tools"] = []any{map[string]any{
			"name": "octopus_capability_probe", "description": "Return a successful capability probe.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}
		payload["tool_choice"] = map[string]any{"type": "tool", "name": "octopus_capability_probe"}
	case model.CapabilityVision:
		payload["messages"] = []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Reply with OK."},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": probePNGBase64}},
			},
		}}
	}
	return payload
}

func geminiProbePayload(capability model.Capability, maxOutputTokens int) map[string]any {
	parts := []any{map[string]any{"text": "Reply with OK."}}
	payload := map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{"maxOutputTokens": maxOutputTokens},
	}
	switch capability {
	case model.CapabilityTool:
		payload["contents"] = []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Call octopus_capability_probe now."}}}}
		payload["tools"] = []any{map[string]any{"functionDeclarations": []any{map[string]any{
			"name": "octopus_capability_probe", "description": "Return a successful capability probe.",
			"parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{}},
		}}}}
		payload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{
			"mode": "ANY", "allowedFunctionNames": []string{"octopus_capability_probe"},
		}}
	case model.CapabilityVision:
		payload["contents"] = []any{map[string]any{"role": "user", "parts": []any{
			map[string]any{"text": "Reply with OK."},
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": probePNGBase64}},
		}}}
	}
	return payload
}

func openAIFunctionTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "octopus_capability_probe", "description": "Return a successful capability probe.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func applyChannelProbeHeaders(request *http.Request, channel *model.Channel) {
	for _, header := range channel.CustomHeader {
		if name := strings.TrimSpace(header.HeaderKey); name != "" && !requestrewrite.IsProtectedHeader(name) {
			request.Header.Set(name, header.HeaderValue)
		}
	}
	for _, rule := range channel.HeaderRules {
		name := strings.TrimSpace(rule.HeaderKey)
		if name == "" || requestrewrite.IsProtectedHeader(name) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "set":
			request.Header.Set(name, rule.HeaderValue)
		case "append":
			request.Header.Add(name, rule.HeaderValue)
		case "remove":
			request.Header.Del(name)
		}
	}
	if userAgent := strings.TrimSpace(channel.UserAgent); userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
}

func responseContainsToolCall(body []byte) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			if call, ok := typed["functionCall"].(map[string]any); ok && len(call) > 0 {
				return true
			}
			if calls, ok := typed["tool_calls"].([]any); ok && len(calls) > 0 {
				return true
			}
			if kind, _ := typed["type"].(string); kind == "function_call" || kind == "tool_use" {
				return true
			}
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}

func classifyProbeFailure(
	code int,
	message string,
	capability model.Capability,
	classification errorclass.Classification,
) (model.CapabilityStatus, string) {
	lower := strings.ToLower(message + " " + classification.Reason)
	if code == http.StatusUnauthorized || code == http.StatusForbidden || containsAny(lower,
		"unauthorized", "not authorized", "permission denied", "access denied", "invalid api key", "authentication") {
		return model.CapabilityUnauthorized, "unauthorized"
	}
	if containsAny(lower, "model not found", "model_not_found", "does not have access", "not available for this account") {
		return model.CapabilityUnauthorized, "model_unauthorized"
	}
	if code == http.StatusTooManyRequests {
		if classification.Level == errorclass.ErrorLevelChannel {
			return model.CapabilityTransient, "rate_limited_channel"
		}
		return model.CapabilityTransient, "rate_limited_key"
	}
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented ||
		containsAny(lower, "not implemented", "not_implemented", "unknown endpoint") {
		return model.CapabilityNotImplemented, "not_implemented"
	}
	if code == http.StatusRequestTimeout || code >= 500 || classification.Level == errorclass.ErrorLevelChannel {
		return model.CapabilityTransient, "upstream_transient"
	}
	if code == http.StatusBadRequest || code == http.StatusUnprocessableEntity {
		if capability == model.CapabilityTool && containsAny(lower, "tool", "function") {
			return model.CapabilityNotImplemented, "tool_not_implemented"
		}
		return model.CapabilityUnsupported, "unsupported_request"
	}
	if code >= 400 && code < 500 || classification.Level == errorclass.ErrorLevelClient {
		return model.CapabilityUnsupported, "unsupported_request"
	}
	if classification.Level == errorclass.ErrorLevelKey {
		return model.CapabilityTransient, "key_transient"
	}
	return model.CapabilityTransient, "unexpected_status"
}

func classifyTransportError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "transport"
}

func transientProbeResult(class string, err error) ProbeResult {
	message := ""
	if err != nil {
		message = safeProbeMessage(err.Error())
	}
	return ProbeResult{
		Status:       model.CapabilityTransient,
		ErrorClass:   class,
		ErrorLevel:   errorclass.ErrorLevelChannel.String(),
		ErrorMessage: message,
	}
}

func extractProviderError(body []byte) string {
	var value any
	if json.Unmarshal(body, &value) == nil {
		if message := findErrorMessage(value); message != "" {
			return message
		}
	}
	return http.StatusText(http.StatusBadRequest)
}

func findErrorMessage(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"message", "detail", "error_description"} {
			if message, ok := typed[key].(string); ok && strings.TrimSpace(message) != "" {
				return message
			}
		}
		if nested, ok := typed["error"]; ok {
			return findErrorMessage(nested)
		}
	case string:
		return typed
	}
	return "provider rejected the capability probe"
}

func safeProbeMessage(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > 256 {
		value = string(runes[:256])
	}
	return value
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
