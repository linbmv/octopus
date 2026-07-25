package selfheal

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestGenerateVariantsBoundsAndDeduplicatesSingleDimensions(t *testing.T) {
	channel := &model.Channel{UserAgent: "old", CustomHeader: []model.CustomHeader{{HeaderKey: "originator", HeaderValue: "old"}}, HeaderRules: []model.HeaderRule{{Action: "set", HeaderKey: "originator", HeaderValue: "old"}}}
	plan := GenerateVariants(channel, llm.APIFormatOpenAIResponse, model.RootCauseProtocolDrift, 16, ExtraCandidates{})
	if plan.EarlyStop || len(plan.Variants) == 0 || len(plan.Variants) > 16 {
		t.Fatalf("variant plan = %#v", plan)
	}
	seen := map[string]struct{}{}
	for _, variant := range plan.Variants {
		if _, exists := seen[variant.ID]; exists {
			t.Fatalf("duplicate variant id %q", variant.ID)
		}
		seen[variant.ID] = struct{}{}
		if variant.Dimension != DimensionBaseline && variant.ParentVariantID != plan.Variants[0].ID {
			t.Fatalf("variant %q is not a single-dimension child: %#v", variant.ID, variant)
		}
		if variant.Dimension == DimensionHeader {
			for key := range variant.HeaderSet {
				if key == "authorization" || key == "x-api-key" {
					t.Fatalf("protected header generated: %#v", variant)
				}
			}
		}
	}
}

func TestGenerateVariantsStopsForCapacityAndAuth(t *testing.T) {
	channel := &model.Channel{UserAgent: "old"}
	for _, cause := range []model.RootCause{model.RootCauseCapacity, model.RootCauseRateLimit, model.RootCauseAuth, model.RootCauseNetwork} {
		plan := GenerateVariants(channel, llm.APIFormatAnthropicMessage, cause, 16, ExtraCandidates{})
		if !plan.EarlyStop || len(plan.Variants) != 1 || plan.Variants[0].Dimension != DimensionBaseline {
			t.Fatalf("cause %s plan = %#v, want baseline-only early stop", cause, plan)
		}
	}
}

func TestVariantNormalizationDropsProtectedHeaders(t *testing.T) {
	variant := Variant{Dimension: DimensionHeader, HeaderSet: map[string]string{"Authorization": "secret", "Originator": "codex"}}
	variant.Normalize()
	if _, ok := variant.HeaderSet["authorization"]; ok {
		t.Fatal("protected header survived variant normalization")
	}
	if variant.ID == "" || variant.HeaderSet["originator"] != "codex" {
		t.Fatalf("normalized variant = %#v", variant)
	}
}

func TestBodyCandidatesArePatchable(t *testing.T) {
	// A field the diagnostic can probe but the patch builder rejects turns a
	// successful diagnosis into a failed session; keep the two in lockstep.
	for _, protocol := range []llm.APIFormat{
		llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact, llm.APIFormatAnthropicMessage,
	} {
		for _, variant := range bodyCandidates(protocol) {
			for name := range variant.BodySet {
				if !safeTopLevelField(name) {
					t.Fatalf("protocol %s: BodySet field %q is probe-able but not patchable", protocol, name)
				}
			}
			for _, name := range variant.BodyDelete {
				if !safeTopLevelField(name) {
					t.Fatalf("protocol %s: BodyDelete field %q is probe-able but not patchable", protocol, name)
				}
			}
		}
	}
}

func TestGenerateVariantsIncludesConfiguredExtraCandidates(t *testing.T) {
	channel := &model.Channel{UserAgent: "current-ua"}
	extra := ExtraCandidates{
		UserAgents: []string{"codex-tui/0.150.0", "current-ua", " "},
		Headers:    []string{"x-new-beta: enabled", "authorization: secret", "malformed-entry"},
	}
	plan := GenerateVariants(channel, llm.APIFormatOpenAIResponse, model.RootCauseProtocolDrift, 16, extra)
	var foundUA, foundHeader bool
	for _, variant := range plan.Variants {
		if variant.UserAgent != nil && *variant.UserAgent == "codex-tui/0.150.0" {
			foundUA = true
		}
		if variant.HeaderSet["x-new-beta"] == "enabled" {
			foundHeader = true
		}
		if variant.HeaderSet["authorization"] != "" {
			t.Fatal("protected extra header survived variant generation")
		}
	}
	if !foundUA || !foundHeader {
		t.Fatalf("extra candidates missing: ua=%v header=%v plan=%#v", foundUA, foundHeader, plan.Variants)
	}
}
