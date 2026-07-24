package selfheal

import (
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/selfheal/importer"
)

func compareSample(t *testing.T, raw string) *importer.Sample {
	t.Helper()
	sample, err := importer.ParseJSONSample([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return sample
}

func TestDiffSampleHeadersReportsAddedAndChanged(t *testing.T) {
	sample := compareSample(t, `{
		"method":"POST","url":"https://provider.test/v1/responses",
		"headers":{"User-Agent":["codex-tui/0.144.6"],"originator":["codex-tui"]},
		"body":{"model":"m","input":"hi"}
	}`)
	artifact := &requestartifact.Artifact{
		Method: "POST", URL: "https://provider.test/v1/responses",
		Headers: map[string]string{"user-agent": "octopus/1.0", "content-type": "application/json"},
	}
	diffs := diffSampleHeaders(sample, artifact)
	joined := strings.Join(diffs, "|")
	if !strings.Contains(joined, "header_added:originator") {
		t.Fatalf("missing originator diff: %v", diffs)
	}
	if !strings.Contains(joined, "header_changed:user-agent") {
		t.Fatalf("missing user-agent diff: %v", diffs)
	}
	if !strings.Contains(joined, "header_removed:content-type") {
		t.Fatalf("missing content-type diff: %v", diffs)
	}
}

func TestDiffSampleHeadersIgnoresPlaceholderValueChanges(t *testing.T) {
	sample := compareSample(t, `{
		"method":"POST","url":"https://provider.test/v1/responses",
		"headers":{"x-custom-tracing":["abc"]}
	}`)
	artifact := &requestartifact.Artifact{
		Method:  "POST",
		Headers: map[string]string{"x-custom-tracing": "[present]"},
	}
	for _, diff := range diffSampleHeaders(sample, artifact) {
		if strings.HasPrefix(diff, "header_changed:x-custom-tracing") {
			t.Fatalf("placeholder header produced a value diff: %v", diff)
		}
	}
}

func TestDiffSampleURLFlagsMethodAndPath(t *testing.T) {
	sample := compareSample(t, `{
		"method":"GET","url":"https://provider.test/v1/other",
		"headers":{}
	}`)
	artifact := &requestartifact.Artifact{Method: "POST", URL: "https://provider.test/v1/responses?key=%5Bredacted%5D"}
	diffs := diffSampleURL(sample, artifact)
	joined := strings.Join(diffs, "|")
	if !strings.Contains(joined, "method:POST!=GET") {
		t.Fatalf("missing method diff: %v", diffs)
	}
	if !strings.Contains(joined, "path:/v1/responses!=/v1/other") {
		t.Fatalf("missing path diff: %v", diffs)
	}
}

func TestSuggestCompareVariantsBuildsBoundedPublicHeaderVariants(t *testing.T) {
	sample := compareSample(t, `{
		"method":"POST","url":"https://provider.test/v1/responses",
		"headers":{
			"User-Agent":["codex-tui/0.144.6"],
			"originator":["codex-tui"],
			"x-codex-beta-features":["remote_compaction_v2"]
		}
	}`)
	diffs := []string{
		"header_added:originator",
		"header_added:x-codex-beta-features",
		"header_changed:user-agent",
	}
	variants := suggestCompareVariants(diffs, sample)
	if len(variants) == 0 || len(variants) > 4 {
		t.Fatalf("unexpected suggestion count: %d", len(variants))
	}
	sawUserAgent := false
	for _, variant := range variants {
		switch variant.Dimension {
		case DimensionUserAgent:
			sawUserAgent = true
			if variant.UserAgent == nil || *variant.UserAgent != "codex-tui/0.144.6" {
				t.Fatalf("user-agent suggestion mismatch: %+v", variant)
			}
		case DimensionHeader:
			for name := range variant.HeaderSet {
				if name == "authorization" || name == "x-api-key" {
					t.Fatalf("suggested a protected header: %+v", variant)
				}
			}
		default:
			t.Fatalf("unexpected suggestion dimension: %s", variant.Dimension)
		}
	}
	if !sawUserAgent {
		t.Fatalf("expected a user-agent suggestion, got %+v", variants)
	}
}

func TestSuggestCompareVariantsSkipsProtectedHeaders(t *testing.T) {
	// A hand-built sample bypasses importer auth rejection to prove the
	// suggestion layer independently refuses protected headers.
	sample := &importer.Sample{Headers: map[string][]string{"X-Auth-Token": {"secret"}}}
	variants := suggestCompareVariants([]string{"header_added:x-auth-token"}, sample)
	if len(variants) != 0 {
		t.Fatalf("protected header produced suggestions: %+v", variants)
	}
}

func TestArtifactURLPath(t *testing.T) {
	cases := map[string]string{
		"https://provider.test/v1/responses":           "/v1/responses",
		"https://provider.test/v1/responses?q=x":       "/v1/responses",
		"https://provider.test":                        "/",
		"/v1/messages":                                 "/v1/messages",
		"":                                             "",
	}
	for input, want := range cases {
		if got := artifactURLPath(input); got != want {
			t.Fatalf("artifactURLPath(%q) = %q, want %q", input, got, want)
		}
	}
}
