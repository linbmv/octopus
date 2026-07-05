package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

const compactProbeTimeout = 15 * time.Second
const compactProbeHandoffText = "Project handoff: The current objective is to keep routing reliable while avoiding provider abuse triggers. Constraint: preserve retry fallback behavior for real user requests. Next step: summarize the routing risk and the mitigation."
const compactProbeSummaryInstruction = "Write a concise continuation summary for this handoff."

type CompactProbeResult struct {
	Strategy model.CompactStrategy
	Error    string
}

func ProbeCompactStrategy(ctx context.Context, channel model.Channel, probeModel string) CompactProbeResult {
	probeModel = strings.TrimSpace(probeModel)
	if probeModel == "" {
		return CompactProbeResult{Error: "compact probe skipped: model is empty"}
	}

	key := channel.GetChannelKey()
	if key.ChannelKey == "" {
		return CompactProbeResult{Error: "compact probe skipped: no available channel key"}
	}

	client, err := ChannelHttpClient(&channel)
	if err != nil {
		return CompactProbeResult{Error: fmt.Sprintf("compact probe skipped: %v", err)}
	}

	strategies := compactProbeStrategyOrder(channel.Type)
	if len(strategies) == 0 {
		return CompactProbeResult{Error: fmt.Sprintf("compact probe skipped: channel type %s is not compatible", channel.Type)}
	}

	var lastErr error
	allDefinitive := true
	for _, strategy := range strategies {
		err := doCompactProbe(ctx, client, channel, key.ChannelKey, probeModel, strategy)
		if err == nil {
			return CompactProbeResult{Strategy: strategy}
		}
		lastErr = err
		if !isCompactProbeDefinitiveIncompatibility(err) {
			allDefinitive = false
		}
		if !canTryNextCompactProbeStrategy(strategy, err) {
			break
		}
	}

	if lastErr == nil {
		return CompactProbeResult{Error: "compact probe failed"}
	}
	if allDefinitive && isCompactProbeDefinitiveIncompatibility(lastErr) {
		return CompactProbeResult{Strategy: model.CompactStrategyIncompatible, Error: lastErr.Error()}
	}
	return CompactProbeResult{Error: lastErr.Error()}
}

func compactProbeStrategyOrder(channelType llm.APIFormat) []model.CompactStrategy {
	return model.CompactStrategyOrder(channelType)
}

func doCompactProbe(ctx context.Context, client *http.Client, channel model.Channel, key, probeModel string, strategy model.CompactStrategy) error {
	reqCtx, cancel := context.WithTimeout(ctx, compactProbeTimeout)
	defer cancel()

	baseURL := transformer.NormalizeBaseURL(channel.GetBaseUrl(), "v1")
	path, body, err := compactProbeRequest(strategy, probeModel)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("compact probe %s failed: %w", strategy, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyCustomHeaders(req, channel)

	resp, err := client.Do(req)
	if err != nil {
		return &compactProbeHTTPError{strategy: strategy, err: err}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && !compactProbeBodyHasError(respBody) {
		return nil
	}

	return &compactProbeHTTPError{
		strategy:   strategy,
		statusCode: resp.StatusCode,
		body:       string(respBody),
	}
}

func compactProbeRequest(strategy model.CompactStrategy, probeModel string) (string, []byte, error) {
	switch strategy {
	case model.CompactStrategyOfficial:
		body, err := json.Marshal(map[string]any{
			"model":             probeModel,
			"input":             compactProbeResponsesInput(compactProbeHandoffText),
			"instructions":      compactProbeSummaryInstruction,
			"max_output_tokens": 16,
		})
		return "/responses/compact", body, err
	case model.CompactStrategyResponsesManual:
		body, err := json.Marshal(map[string]any{
			"model":             probeModel,
			"input":             compactProbeResponsesInput(compactProbeHandoffText),
			"store":             false,
			"max_output_tokens": 16,
		})
		return "/responses", body, err
	case model.CompactStrategyChatManual:
		body, err := json.Marshal(map[string]any{
			"model":      probeModel,
			"messages":   []map[string]string{{"role": "user", "content": compactProbeSummaryInstruction + " " + compactProbeHandoffText}},
			"stream":     false,
			"max_tokens": 16,
		})
		return "/chat/completions", body, err
	default:
		return "", nil, fmt.Errorf("unknown compact probe strategy: %s", strategy)
	}
}

func compactProbeResponsesInput(text string) []map[string]any {
	return []map[string]any{
		{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{
					"type": "input_text",
					"text": text,
				},
			},
		},
	}
}

type compactProbeHTTPError struct {
	strategy   model.CompactStrategy
	statusCode int
	body       string
	err        error
}

func (e *compactProbeHTTPError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return fmt.Sprintf("compact probe %s failed: %v", e.strategy, e.err)
	}
	body := strings.TrimSpace(e.body)
	if body == "" {
		body = http.StatusText(e.statusCode)
	}
	return fmt.Sprintf("compact probe %s failed: status=%d body=%s", e.strategy, e.statusCode, compactProbeErrorSnippet(body))
}

func compactProbeErrorSnippet(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 300 {
		return value[:300]
	}
	return value
}

func canTryNextCompactProbeStrategy(strategy model.CompactStrategy, err error) bool {
	switch strategy {
	case model.CompactStrategyOfficial:
		return isCompactProbeEndpointUnsupported(err) ||
			isCompactProbeResponsesIncompatible(err) ||
			isCompactProbeRetryable(err)
	case model.CompactStrategyResponsesManual:
		return isCompactProbeEndpointUnsupported(err) ||
			isCompactProbeResponsesIncompatible(err) ||
			isCompactProbeRetryable(err)
	default:
		return false
	}
}

func isCompactProbeDefinitiveIncompatibility(err error) bool {
	return isCompactProbeEndpointUnsupported(err) || isCompactProbeResponsesIncompatible(err)
}

func isCompactProbeEndpointUnsupported(err error) bool {
	probeErr, ok := err.(*compactProbeHTTPError)
	if !ok || probeErr == nil {
		return false
	}
	switch probeErr.statusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	body := strings.ToLower(probeErr.body)
	for _, marker := range []string{
		"invalid url",
		"no such endpoint",
		"unknown endpoint",
		"route not found",
		"cannot post",
		"unsupported endpoint",
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func isCompactProbeResponsesIncompatible(err error) bool {
	probeErr, ok := err.(*compactProbeHTTPError)
	if !ok || probeErr == nil {
		return false
	}
	if probeErr.statusCode != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(probeErr.body)
	return strings.Contains(body, "invalid_responses_request") &&
		strings.Contains(body, "invalid codex request")
}

func isCompactProbeRetryable(err error) bool {
	probeErr, ok := err.(*compactProbeHTTPError)
	if !ok || probeErr == nil {
		return false
	}
	switch probeErr.statusCode {
	case http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	if probeErr.err != nil {
		msg := strings.ToLower(probeErr.err.Error())
		return strings.Contains(msg, "connection reset by peer") ||
			strings.Contains(msg, "unexpected eof") ||
			strings.Contains(msg, "context deadline exceeded")
	}
	body := strings.ToLower(probeErr.body)
	return strings.Contains(body, "bad gateway") ||
		strings.Contains(body, "gateway timeout") ||
		strings.Contains(body, "service unavailable") ||
		strings.Contains(body, "connection reset by peer") ||
		strings.Contains(body, "unexpected eof")
}

func compactProbeBodyHasError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return false
	}
	if _, ok := data["error"]; ok {
		return true
	}
	if value, ok := data["type"].(string); ok && strings.EqualFold(value, "error") {
		return true
	}
	return false
}
