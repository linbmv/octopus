package selfheal

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/selfheal/importer"
)

// CompareRequest carries a redacted golden sample to diff against the exact
// request Octopus would send for the channel today. No upstream traffic and
// no cost budget are involved; the comparison is purely structural.
type CompareRequest struct {
	ChannelID    int    `json:"channel_id"`
	ChannelKeyID int    `json:"channel_key_id,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Model        string `json:"model,omitempty"`
	GoldenSample []byte `json:"golden_sample"`
}

type CompareResult struct {
	Sample     *importer.Sample            `json:"sample"`
	Artifact   *requestartifact.Artifact   `json:"artifact"`
	HeaderDiff []string                    `json:"header_diff"`
	BodyDiff   []string                    `json:"body_diff"`
	URLDiff    []string                    `json:"url_diff"`
	Suggested  []Variant                   `json:"suggested_variants,omitempty"`
}

// Compare parses the golden sample, builds the channel's current outbound
// request without contacting the upstream, and reports per-field differences.
// Suggested variants are limited to allowlisted, non-auth header changes the
// diagnostic matrix could try next.
func (w *Worker) Compare(ctx context.Context, request CompareRequest) (*CompareResult, error) {
	if w == nil || !w.config.Enabled {
		return nil, ErrSelfHealingDisabled
	}
	sample, err := importer.ParseJSONSample(request.GoldenSample)
	if err != nil {
		return nil, err
	}
	channel, key, endpoint, modelName, err := resolveDiagnosticScope(ctx, request.ChannelID, request.ChannelKeyID, request.Endpoint, request.Model)
	if err != nil {
		return nil, err
	}
	if !channel.SelfHealingEnabled {
		return nil, ErrChannelSelfHealingDisabled
	}
	internalRequest := minimalDiagnosticRequest(channel.Type, modelName)
	baseline := Variant{Dimension: DimensionBaseline, Description: "current channel configuration"}
	baseline.Normalize()
	built, err := BuildVariantRequest(ctx, channel, key, endpoint, internalRequest, baseline)
	if err != nil {
		return nil, err
	}
	result := &CompareResult{
		Sample:     sample,
		Artifact:   built.Artifact,
		HeaderDiff: diffSampleHeaders(sample, built.Artifact),
		BodyDiff:   importer.DiffBodyKeys(built.Artifact.Body.TopLevelKeys, sample.BodyKeys),
		URLDiff:    diffSampleURL(sample, built.Artifact),
	}
	result.Suggested = suggestCompareVariants(result.HeaderDiff, sample)
	return result, nil
}

// diffSampleHeaders compares header names and, when the artifact retained a
// concrete value, header values. Artifact placeholders ("[present]",
// "[redacted]") only participate in presence checks so redaction never shows
// up as a false value change. Auth headers are excluded on both sides.
func diffSampleHeaders(sample *importer.Sample, artifact *requestartifact.Artifact) []string {
	if sample == nil || artifact == nil {
		return nil
	}
	artifactHeaders := make(map[string][]string, len(artifact.Headers))
	placeholders := make(map[string]struct{})
	for name, value := range artifact.Headers {
		name = strings.ToLower(strings.TrimSpace(name))
		if value == "[present]" || value == "[redacted]" {
			// Keep a non-empty value so presence checks still see the header;
			// the placeholder set below suppresses the bogus value diff.
			placeholders[name] = struct{}{}
			artifactHeaders[name] = []string{value}
			continue
		}
		artifactHeaders[name] = []string{value}
	}
	sampleHeaders := make(map[string][]string, len(sample.Headers))
	for name, values := range sample.Headers {
		sampleHeaders[strings.ToLower(strings.TrimSpace(name))] = values
	}
	diffs := make([]string, 0, 8)
	for _, diff := range importer.DiffHeaders(artifactHeaders, sampleHeaders) {
		// A placeholder header has an unknown value; drop value-change noise
		// for it but keep genuine added/removed findings.
		if name, ok := strings.CutPrefix(diff, "header_changed:"); ok {
			if _, placeholder := placeholders[name]; placeholder {
				continue
			}
		}
		diffs = append(diffs, diff)
	}
	sort.Strings(diffs)
	return diffs
}

func diffSampleURL(sample *importer.Sample, artifact *requestartifact.Artifact) []string {
	if sample == nil || artifact == nil {
		return nil
	}
	diffs := make([]string, 0, 2)
	if !strings.EqualFold(strings.TrimSpace(sample.Method), strings.TrimSpace(artifact.Method)) {
		diffs = append(diffs, fmt.Sprintf("method:%s!=%s", artifact.Method, sample.Method))
	}
	samplePath := strings.TrimSpace(sample.Path)
	artifactPath := artifactURLPath(artifact.URL)
	if samplePath != "" && artifactPath != "" && samplePath != artifactPath {
		diffs = append(diffs, fmt.Sprintf("path:%s!=%s", artifactPath, samplePath))
	}
	return diffs
}

func artifactURLPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Path == "" {
		return "/"
	}
	return parsed.Path
}

// suggestCompareVariants converts golden-sample header differences into
// bounded diagnostic variants. Only added/changed public headers become
// suggestions; removals and body drift stay evidence-only because deleting a
// header the sample lacks is already covered by the standard matrix.
func suggestCompareVariants(headerDiff []string, sample *importer.Sample) []Variant {
	if sample == nil {
		return nil
	}
	sampleValues := make(map[string]string, len(sample.Headers))
	for name, values := range sample.Headers {
		if len(values) == 0 {
			continue
		}
		// HTTP allows joining repeated header values with commas; a suggested
		// variant must carry all of them to actually replicate the sample.
		joined := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				joined = append(joined, value)
			}
		}
		sampleValues[strings.ToLower(strings.TrimSpace(name))] = strings.Join(joined, ", ")
	}
	suggested := make([]Variant, 0, 4)
	for _, diff := range headerDiff {
		name, added := strings.CutPrefix(diff, "header_added:")
		if !added {
			name, added = strings.CutPrefix(diff, "header_changed:")
		}
		if !added {
			continue
		}
		value, ok := sampleValues[name]
		if !ok || value == "" {
			continue
		}
		variant := Variant{
			Dimension:   DimensionHeader,
			Description: "match golden sample header " + name,
			HeaderSet:   map[string]string{name: value},
		}
		variant.Normalize()
		// Normalize drops protected headers; skip variants it emptied out.
		if len(variant.HeaderSet) == 0 {
			continue
		}
		if name == "user-agent" {
			variant = Variant{Dimension: DimensionUserAgent, Description: "match golden sample User-Agent", UserAgent: &value}
			variant.Normalize()
		}
		suggested = append(suggested, variant)
		if len(suggested) >= 4 {
			break
		}
	}
	return suggested
}
