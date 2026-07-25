package selfheal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm"
)

const (
	MaxDiagnosticVariants = 16
	DimensionBaseline     = "baseline"
	DimensionUserAgent    = "user_agent"
	DimensionHeader       = "header"
	DimensionBody         = "body"
)

// Variant describes a single, independently attributable request change. The
// fields are deltas over the current channel configuration; the request
// builder applies them after the normal relay rewrite pipeline.
type Variant struct {
	ID              string            `json:"variant_id"`
	ParentVariantID string            `json:"parent_variant_id,omitempty"`
	Dimension       string            `json:"dimension"`
	Description     string            `json:"description"`
	UserAgent       *string           `json:"user_agent,omitempty"`
	HeaderSet       map[string]string `json:"header_set,omitempty"`
	HeaderRemove    []string          `json:"header_remove,omitempty"`
	BodySet         map[string]any    `json:"body_set,omitempty"`
	BodyDelete      []string          `json:"body_delete,omitempty"`
}

type VariantPlan struct {
	Variants   []Variant       `json:"variants"`
	EarlyStop  bool            `json:"early_stop"`
	StopCause  model.RootCause `json:"stop_cause,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
}

func (v Variant) canonical() any {
	return struct {
		ParentVariantID string
		Dimension       string
		UserAgent       *string
		HeaderSet       map[string]string
		HeaderRemove    []string
		BodySet         map[string]any
		BodyDelete      []string
	}{v.ParentVariantID, v.Dimension, v.UserAgent, v.HeaderSet,
		append([]string(nil), v.HeaderRemove...), v.BodySet, append([]string(nil), v.BodyDelete...)}
}

func (v *Variant) Normalize() {
	if v == nil {
		return
	}
	v.ParentVariantID = strings.TrimSpace(v.ParentVariantID)
	v.Dimension = strings.TrimSpace(v.Dimension)
	v.Description = boundedReason(v.Description)
	if v.UserAgent != nil {
		value := strings.TrimSpace(*v.UserAgent)
		v.UserAgent = &value
	}
	if v.HeaderSet != nil {
		clean := make(map[string]string, len(v.HeaderSet))
		for name, value := range v.HeaderSet {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" || requestrewrite.IsProtectedHeader(name) {
				continue
			}
			clean[name] = boundedReason(value)
		}
		v.HeaderSet = clean
	}
	v.HeaderRemove = normalizeHeaderNames(v.HeaderRemove)
	v.BodyDelete = normalizeNames(v.BodyDelete)
	if v.ID == "" {
		encoded, _ := json.Marshal(v.canonical())
		sum := sha256.Sum256(encoded)
		v.ID = "v_" + hex.EncodeToString(sum[:])[:16]
	}
}

func normalizeHeaderNames(values []string) []string {
	values = normalizeNames(values)
	result := values[:0]
	for _, value := range values {
		if !requestrewrite.IsProtectedHeader(value) {
			result = append(result, value)
		}
	}
	return result
}

func normalizeNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// GenerateVariants creates a bounded, deduplicated matrix. Capacity, quota,
// auth, endpoint, and network observations stop after the baseline because no
// header/body change can repair those failures.
func GenerateVariants(channel *model.Channel, protocol llm.APIFormat, rootCause model.RootCause, maxVariants int) VariantPlan {
	if maxVariants <= 0 || maxVariants > MaxDiagnosticVariants {
		maxVariants = MaxDiagnosticVariants
	}
	plan := VariantPlan{Variants: []Variant{{Dimension: DimensionBaseline, Description: "current channel configuration"}}}
	plan.Variants[0].Normalize()
	if earlyStopRootCause(rootCause) {
		plan.EarlyStop = true
		plan.StopCause = rootCause
		plan.StopReason = fmt.Sprintf("%s does not justify protocol or client-fingerprint variants", rootCause)
		return plan
	}
	if channel == nil || rootCause == model.RootCauseUnknown || rootCause == model.RootCauseNone {
		return plan
	}
	seen := map[string]struct{}{plan.Variants[0].ID: {}}
	add := func(variant Variant) {
		if len(plan.Variants) >= maxVariants {
			return
		}
		variant.ParentVariantID = plan.Variants[0].ID
		variant.Normalize()
		if _, exists := seen[variant.ID]; exists {
			return
		}
		seen[variant.ID] = struct{}{}
		plan.Variants = append(plan.Variants, variant)
	}

	for _, value := range userAgentCandidates(channel, protocol) {
		add(Variant{Dimension: DimensionUserAgent, Description: "try a known client User-Agent", UserAgent: &value})
	}
	for _, header := range headerCandidates(channel, protocol) {
		add(header)
	}
	for _, body := range bodyCandidates(protocol) {
		add(body)
	}
	return plan
}

func earlyStopRootCause(rootCause model.RootCause) bool {
	switch rootCause {
	case model.RootCauseCapacity, model.RootCauseRateLimit, model.RootCauseAuth,
		model.RootCauseEndpoint, model.RootCauseNetwork, model.RootCauseModelAccess, model.RootCauseDecode:
		return true
	default:
		return false
	}
}

func userAgentCandidates(channel *model.Channel, protocol llm.APIFormat) []string {
	if channel == nil {
		return nil
	}
	values := make([]string, 0, 3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == channel.UserAgent {
			return
		}
		for _, existing := range values {
			if existing == value {
				return
			}
		}
		values = append(values, value)
	}
	switch protocol {
	case llm.APIFormatAnthropicMessage:
		add("claude-cli")
		add("claude-code")
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		add("codex-tui/0.144.6")
		add("codex-cli")
	default:
		add("openai-python")
	}
	return values
}

func headerCandidates(channel *model.Channel, protocol llm.APIFormat) []Variant {
	result := make([]Variant, 0, 8)
	seen := make(map[string]struct{})
	add := func(v Variant) {
		for name := range v.HeaderSet {
			if requestrewrite.IsProtectedHeader(name) {
				return
			}
		}
		v.Dimension = DimensionHeader
		v.Normalize()
		key, _ := json.Marshal(v.canonical())
		if _, ok := seen[string(key)]; ok {
			return
		}
		seen[string(key)] = struct{}{}
		result = append(result, v)
	}
	if channel != nil {
		for _, header := range channel.CustomHeader {
			if !requestrewrite.IsProtectedHeader(header.HeaderKey) {
				add(Variant{Description: "remove existing public header " + strings.ToLower(strings.TrimSpace(header.HeaderKey)), HeaderRemove: []string{header.HeaderKey}})
			}
		}
		for _, header := range channel.HeaderRules {
			if !requestrewrite.IsProtectedHeader(header.HeaderKey) {
				add(Variant{Description: "remove existing public header rule " + strings.ToLower(strings.TrimSpace(header.HeaderKey)), HeaderRemove: []string{header.HeaderKey}})
			}
		}
	}
	switch protocol {
	case llm.APIFormatAnthropicMessage:
		add(Variant{Description: "add Anthropic beta client header", HeaderSet: map[string]string{"anthropic-beta": "prompt-caching-2024-07-31"}})
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		add(Variant{Description: "add Codex originator header", HeaderSet: map[string]string{"originator": "codex-tui"}})
		add(Variant{Description: "add Codex beta feature header", HeaderSet: map[string]string{"x-codex-beta-features": "remote_compaction_v2"}})
	}
	return result
}

// patchableBodyFields is the single allowlist shared by variant generation
// (bodyCandidates) and patch synthesis (safeTopLevelField). A field must never
// be probe-able without also being patchable, otherwise a successful diagnostic
// fails at patch time and the session is recorded as failed.
var patchableBodyFields = map[string]struct{}{
	"stream": {}, "store": {}, "reasoning": {}, "include": {}, "instructions": {}, "thinking": {},
}

func bodyCandidates(protocol llm.APIFormat) []Variant {
	result := make([]Variant, 0, 8)
	add := func(description string, set map[string]any, remove []string) {
		result = append(result, Variant{Dimension: DimensionBody, Description: description, BodySet: set, BodyDelete: remove})
	}
	add("enable streaming field", map[string]any{"stream": true}, nil)
	add("remove store field", nil, []string{"store"})
	if protocol == llm.APIFormatOpenAIResponse || protocol == llm.APIFormatOpenAIResponseCompact {
		add("remove reasoning field", nil, []string{"reasoning"})
		add("remove include field", nil, []string{"include"})
		add("remove instructions field", nil, []string{"instructions"})
	}
	if protocol == llm.APIFormatAnthropicMessage {
		add("remove thinking field", nil, []string{"thinking"})
	}
	return result
}
