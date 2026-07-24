package selfheal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

type BuiltRequest struct {
	Request   *httpclient.Request
	Artifact  *requestartifact.Artifact
	ShapeDiff []string
}

const DiagnosticMaxOutputTokens int64 = 8

func BuildVariantRequest(
	ctx context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint string,
	internalRequest *llm.Request,
	variant Variant,
) (*BuiltRequest, error) {
	finalRequest, err := relay.BuildFinalOutboundRequest(ctx, channel, key, endpoint, internalRequest)
	if err != nil {
		return nil, err
	}
	baseline := finalRequest.Artifact
	variant.Normalize()
	if variant.Dimension != DimensionBaseline {
		if err := ApplyVariant(finalRequest.Request, variant); err != nil {
			return nil, err
		}
	}
	if err := EnforceDiagnosticTokenBudget(finalRequest.Request, DiagnosticMaxOutputTokens); err != nil {
		return nil, err
	}
	rewrite := baseline.Rewrite
	if variant.Dimension != DimensionBaseline {
		rewrite.RequestRewriteApplied = true
	}
	artifact := requestartifact.Build(finalRequest.Request, string(channel.Type), internalRequest.Model, rewrite)
	return &BuiltRequest{Request: finalRequest.Request, Artifact: artifact, ShapeDiff: DiffArtifacts(baseline, artifact)}, nil
}

// EnforceDiagnosticTokenBudget runs after every production rewrite and
// diagnostic variant. Channel ParamOverride/JSON rules are therefore unable
// to turn a low-cost synthetic probe into a large completion.
func EnforceDiagnosticTokenBudget(request *httpclient.Request, maxOutputTokens int64) error {
	if request == nil || maxOutputTokens <= 0 {
		return errors.New("diagnostic token budget is invalid")
	}
	contentType := request.ContentType
	if request.Headers != nil {
		contentType += " " + request.Headers.Get("Content-Type")
	}
	contentType = strings.ToLower(contentType)
	if !strings.Contains(contentType, "json") {
		return errors.New("diagnostic requests must use a JSON body")
	}
	if len(request.Body) > requestartifact.MaxBodyBytes {
		return fmt.Errorf("diagnostic request body exceeds %d-byte token-policy limit", requestartifact.MaxBodyBytes)
	}
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(request.Body)))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return fmt.Errorf("decode diagnostic request for token policy: %w", err)
	}
	for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens", "maxOutputTokens"} {
		if _, exists := body[field]; exists {
			body[field] = maxOutputTokens
		}
	}
	if generation, ok := body["generationConfig"].(map[string]any); ok {
		generation["maxOutputTokens"] = maxOutputTokens
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		if _, exists := thinking["budget_tokens"]; exists {
			thinking["budget_tokens"] = maxOutputTokens
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode diagnostic request after token policy: %w", err)
	}
	request.Body = encoded
	return nil
}

func ApplyVariant(request *httpclient.Request, variant Variant) error {
	if request == nil {
		return errors.New("outbound request is required")
	}
	variant.Normalize()
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	if variant.UserAgent != nil {
		request.Headers.Set("User-Agent", *variant.UserAgent)
	}
	for _, name := range variant.HeaderRemove {
		if requestrewrite.IsProtectedHeader(name) {
			return fmt.Errorf("protected header %q cannot be changed by a diagnostic variant", name)
		}
		request.Headers.Del(name)
	}
	for name, value := range variant.HeaderSet {
		if requestrewrite.IsProtectedHeader(name) {
			return fmt.Errorf("protected header %q cannot be changed by a diagnostic variant", name)
		}
		request.Headers.Set(name, value)
	}
	if len(variant.BodySet) == 0 && len(variant.BodyDelete) == 0 {
		return nil
	}
	contentType := strings.ToLower(request.ContentType + " " + request.Headers.Get("Content-Type"))
	if !strings.Contains(contentType, "json") {
		return errors.New("body variants require a JSON outbound request")
	}
	if len(request.Body) > requestartifact.MaxBodyBytes {
		return fmt.Errorf("diagnostic request body exceeds %d-byte variant limit", requestartifact.MaxBodyBytes)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		return fmt.Errorf("decode diagnostic request body: %w", err)
	}
	for _, name := range variant.BodyDelete {
		delete(body, name)
	}
	for name, value := range variant.BodySet {
		body[name] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode diagnostic request body: %w", err)
	}
	request.Body = encoded
	return nil
}

func DiffArtifacts(before, after *requestartifact.Artifact) []string {
	if before == nil || after == nil {
		return nil
	}
	diff := make([]string, 0, 16)
	if before.URL != after.URL {
		diff = append(diff, "url")
	}
	if before.Model != after.Model {
		diff = append(diff, "model")
	}
	appendMapDiff := func(prefix string, left, right map[string]string) {
		seen := make(map[string]struct{}, len(left)+len(right))
		for key := range left {
			seen[key] = struct{}{}
		}
		for key := range right {
			seen[key] = struct{}{}
		}
		for key := range seen {
			if left[key] != right[key] {
				diff = append(diff, prefix+key)
			}
		}
	}
	appendMapDiff("header:", before.Headers, after.Headers)
	appendMapDiff("body:", before.Body.Paths, after.Body.Paths)
	return normalizeDiff(diff)
}

func normalizeDiff(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	slices.Sort(result)
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}
