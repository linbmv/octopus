package selfheal

import (
	"github.com/bestruirui/octopus/internal/model"
)

// ComputeConfidence grades a candidate variant by evidence strength. Only a
// single-dimension variant that succeeded repeatedly earns high confidence;
// a single success is medium, and anything outside the known dimensions
// (future combination variants) is capped at low so a lucky combination can
// never look authoritative.
func ComputeConfidence(variant Variant, attempts []model.DiagnosticAttempt) model.PatchConfidence {
	if !singleDimension(variant.Dimension) {
		return model.PatchConfidenceLow
	}
	successes := 0
	for _, attempt := range attempts {
		if attempt.VariantID == variant.ID && attempt.Success {
			successes++
		}
	}
	switch {
	case successes >= 2:
		return model.PatchConfidenceHigh
	case successes == 1:
		return model.PatchConfidenceMedium
	default:
		return model.PatchConfidenceLow
	}
}

func singleDimension(dimension string) bool {
	switch dimension {
	case DimensionUserAgent, DimensionHeader, DimensionBody:
		return true
	default:
		return false
	}
}
