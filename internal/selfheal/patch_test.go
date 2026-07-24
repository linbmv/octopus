package selfheal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestartifact"
)

func TestGenerateCandidatePatchRequiresBaselineFailureAndSingleVariantSuccess(t *testing.T) {
	channel := &model.Channel{ID: 4, Name: "channel", Type: "openai/responses", Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test"}}, Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "secret"}}, ConfigVersion: 3}
	session := &model.DiagnosticSession{ID: "diag", ChannelID: 4, ChannelKeyID: 1, Model: "model-a", WireProtocol: "openai/responses",
		Endpoint: "https://provider.test", EndpointFingerprint: "endpoint", ScopeFingerprint: "scope", ConfigVersion: 3}
	ua := "codex-tui/1"
	baseline := Variant{Dimension: DimensionBaseline}
	baseline.Normalize()
	variant := Variant{Dimension: DimensionUserAgent, UserAgent: &ua}
	variant.ParentVariantID = baseline.ID
	variant.Normalize()
	plan := VariantPlan{Variants: []Variant{baseline, variant}}
	attempts := []model.DiagnosticAttempt{
		{VariantID: baseline.ID, Success: false, RequestShape: requestartifact.Artifact{ShapeSHA256: "base"}},
		{VariantID: variant.ID, Success: true, RequestShape: requestartifact.Artifact{ShapeSHA256: "ua"}},
	}
	patch, err := GenerateCandidatePatch(session, channel, plan, attempts)
	if err != nil {
		t.Fatalf("GenerateCandidatePatch error: %v", err)
	}
	if patch.AfterSnapshot.UserAgent != ua || patch.Confidence != model.PatchConfidenceMedium || len(patch.Changes) != 1 || patch.Changes[0].Field != "user_agent" {
		t.Fatalf("generated patch = %#v", patch)
	}
	encoded := string(mustJSONForPatch(patch))
	if strings.Contains(encoded, "secret") || strings.Contains(encoded, "channel_key") {
		t.Fatalf("patch leaked key material: %s", encoded)
	}
}

func TestGenerateCandidatePatchRejectsBaselineSuccessAndProtectedHeaders(t *testing.T) {
	channel := &model.Channel{ID: 4, Name: "channel", Type: "openai/responses", Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test"}}, Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "secret"}}, ConfigVersion: 3}
	session := &model.DiagnosticSession{ID: "diag", ChannelID: 4, ScopeFingerprint: "scope", ConfigVersion: 3, Model: "m", EndpointFingerprint: "endpoint"}
	baseline := Variant{Dimension: DimensionBaseline}
	baseline.Normalize()
	if _, err := GenerateCandidatePatch(session, channel, VariantPlan{Variants: []Variant{baseline}}, []model.DiagnosticAttempt{{VariantID: baseline.ID, Success: true}}); err != ErrNoPatchCandidate {
		t.Fatalf("baseline success error = %v, want no candidate", err)
	}
	variant := Variant{Dimension: DimensionHeader, HeaderSet: map[string]string{"authorization": "secret"}}
	variant.ParentVariantID = baseline.ID
	variant.Normalize()
	if _, err := GenerateCandidatePatch(session, channel, VariantPlan{Variants: []Variant{baseline, variant}}, []model.DiagnosticAttempt{{VariantID: baseline.ID}, {VariantID: variant.ID, Success: true}}); err != ErrNoPatchCandidate {
		t.Fatalf("protected header candidate error = %v, want no candidate", err)
	}
}

func mustJSONForPatch(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
