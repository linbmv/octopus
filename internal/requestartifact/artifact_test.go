package requestartifact

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm/httpclient"
)

func TestBuildRedactsCredentialsAndRetainsProtocolFingerprint(t *testing.T) {
	artifact := Build(&httpclient.Request{
		Method: "post",
		URL:    "https://provider.test/v1/responses?key=secret&model=gpt-5",
		Headers: http.Header{
			"Authorization":         {"Bearer secret-token"},
			"X-Api-Key":             {"secret-key"},
			"User-Agent":            {"codex-tui/0.144.6"},
			"Originator":            {"codex-tui"},
			"X-Codex-Beta-Features": {"remote_compaction_v2"},
			"X-Private-Header":      {"do-not-copy"},
		},
		ContentType: "application/json",
		Body:        []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"private prompt"}],"stream":true}`),
	}, "openai/responses", "fallback-model", RewriteSummary{RawPassthrough: true})

	if artifact == nil {
		t.Fatal("Build returned nil")
	}
	if artifact.Method != "POST" || artifact.Protocol != "openai/responses" {
		t.Fatalf("basic request metadata = %#v", artifact)
	}
	if artifact.Model != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want request model", artifact.Model)
	}
	if strings.Contains(artifact.URL, "secret") {
		t.Fatalf("URL leaked query secret: %q", artifact.URL)
	}
	if got := artifact.Headers["authorization"]; got != "[redacted]" {
		t.Fatalf("authorization = %q", got)
	}
	if got := artifact.Headers["x-api-key"]; got != "[redacted]" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := artifact.Headers["user-agent"]; got != "codex-tui/0.144.6" {
		t.Fatalf("user-agent = %q", got)
	}
	if got := artifact.Headers["x-private-header"]; got != "[present]" {
		t.Fatalf("private header = %q", got)
	}
	if artifact.Body.Paths["/input/[]/content"] != "string" {
		t.Fatalf("body paths = %#v", artifact.Body.Paths)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if strings.Contains(string(encoded), "private prompt") {
		t.Fatalf("artifact retained private body content: %s", encoded)
	}
}

func TestBuildShapeHashDoesNotDependOnUserContent(t *testing.T) {
	base := func(prompt string) *Artifact {
		return Build(&httpclient.Request{
			Method:      "POST",
			URL:         "https://provider.test/v1/chat/completions",
			Headers:     http.Header{"Content-Type": {"application/json"}},
			ContentType: "application/json",
			Body:        []byte(`{"model":"m","messages":[{"role":"user","content":"` + prompt + `"}]}`),
		}, "openai/chat", "m", RewriteSummary{})
	}
	first := base("first private prompt")
	second := base("second private prompt")
	if first.ShapeSHA256 != second.ShapeSHA256 {
		t.Fatalf("shape hash changed with user content: %s != %s", first.ShapeSHA256, second.ShapeSHA256)
	}
}

func TestBuildBoundsLargeBodyWithoutParsingIt(t *testing.T) {
	body := strings.Repeat("x", MaxBodyBytes+1)
	artifact := Build(&httpclient.Request{Body: []byte(body), ContentType: "text/plain"}, "test", "", RewriteSummary{})
	if artifact == nil || !artifact.Body.Truncated {
		t.Fatalf("large body artifact = %#v", artifact)
	}
	if artifact.Body.Paths != nil {
		t.Fatalf("large body should not retain parsed paths: %#v", artifact.Body.Paths)
	}
}
