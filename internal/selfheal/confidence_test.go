package selfheal

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestComputeConfidenceRequiresSingleDimension(t *testing.T) {
	variant := Variant{Dimension: DimensionHeader, HeaderSet: map[string]string{"originator": "codex-tui"}}
	variant.Normalize()
	attempts := []model.DiagnosticAttempt{
		{VariantID: variant.ID, Success: true},
	}
	if got := ComputeConfidence(variant, attempts); got != model.PatchConfidenceMedium {
		t.Fatalf("single success confidence = %s, want medium", got)
	}
	attempts = append(attempts, model.DiagnosticAttempt{VariantID: variant.ID, Success: true})
	if got := ComputeConfidence(variant, attempts); got != model.PatchConfidenceHigh {
		t.Fatalf("repeat success confidence = %s, want high", got)
	}
}

func TestComputeConfidenceCapsUnknownDimensionsAtLow(t *testing.T) {
	variant := Variant{Dimension: "combined"}
	variant.Normalize()
	attempts := []model.DiagnosticAttempt{
		{VariantID: variant.ID, Success: true},
		{VariantID: variant.ID, Success: true},
	}
	if got := ComputeConfidence(variant, attempts); got != model.PatchConfidenceLow {
		t.Fatalf("combination confidence = %s, want low", got)
	}
	baseline := Variant{Dimension: DimensionBaseline}
	baseline.Normalize()
	if got := ComputeConfidence(baseline, attempts); got != model.PatchConfidenceLow {
		t.Fatalf("baseline confidence = %s, want low", got)
	}
}

func TestComputeConfidenceWithoutSuccessesIsLow(t *testing.T) {
	variant := Variant{Dimension: DimensionBody, BodyDelete: []string{"store"}}
	variant.Normalize()
	attempts := []model.DiagnosticAttempt{
		{VariantID: variant.ID, Success: false},
		{VariantID: "other", Success: true},
	}
	if got := ComputeConfidence(variant, attempts); got != model.PatchConfidenceLow {
		t.Fatalf("failed variant confidence = %s, want low", got)
	}
}
