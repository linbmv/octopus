package importer

import (
	"strings"
	"testing"
)

func TestParseJSONSampleRejectsAuthAndLocalhost(t *testing.T) {
	t.Parallel()

	_, err := ParseJSONSample([]byte(`{
		"method":"POST",
		"url":"https://provider.test/v1/messages",
		"headers":{"Authorization":["Bearer sk-test"]},
		"body":{"model":"claude"}
	}`))
	if err == nil || !strings.Contains(err.Error(), "authentication") && err != ErrSampleAuthSecret && !strings.Contains(err.Error(), ErrSampleAuthSecret.Error()) {
		t.Fatalf("expected auth rejection, got %v", err)
	}

	_, err = ParseJSONSample([]byte(`{
		"url":"http://127.0.0.1/v1/messages",
		"headers":{"User-Agent":["codex-tui/0.1"]},
		"body":{"model":"gpt"}
	}`))
	if err != ErrSampleInvalidURL {
		t.Fatalf("expected invalid URL, got %v", err)
	}
}

func TestParseJSONSampleKeepsPublicShape(t *testing.T) {
	t.Parallel()

	sample, err := ParseJSONSample([]byte(`{
		"method":"post",
		"url":"https://provider.test/v1/responses",
		"headers":{"User-Agent":["codex-tui/0.144.6"],"originator":["codex"]},
		"body":{"model":"gpt","input":[],"instructions":"hi"},
		"source":"cli"
	}`))
	if err != nil {
		t.Fatalf("ParseJSONSample() error = %v", err)
	}
	if sample.Method != "POST" || sample.Path != "/v1/responses" {
		t.Fatalf("sample = %+v", sample)
	}
	if sample.Headers["User-Agent"][0] != "codex-tui/0.144.6" {
		t.Fatalf("headers = %+v", sample.Headers)
	}
	if sample.BodyShape["input"] != "array" || sample.BodyShape["instructions"] != "string" {
		t.Fatalf("body shape = %+v", sample.BodyShape)
	}
	keys := map[string]struct{}{}
	for _, key := range sample.BodyKeys {
		keys[key] = struct{}{}
	}
	for _, want := range []string{"model", "input", "instructions"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("missing body key %q in %v", want, sample.BodyKeys)
		}
	}
}

func TestDiffHelpers(t *testing.T) {
	t.Parallel()

	headerDiffs := DiffHeaders(
		map[string][]string{"User-Agent": {"a"}, "Authorization": {"secret"}},
		map[string][]string{"User-Agent": {"b"}, "originator": {"codex"}},
	)
	joined := strings.Join(headerDiffs, ",")
	if !strings.Contains(joined, "header_changed:user-agent") || !strings.Contains(joined, "header_added:originator") {
		t.Fatalf("header diffs = %v", headerDiffs)
	}
	if strings.Contains(joined, "authorization") {
		t.Fatalf("auth header leaked into diffs: %v", headerDiffs)
	}

	bodyDiffs := DiffBodyKeys([]string{"model", "messages"}, []string{"model", "input"})
	joined = strings.Join(bodyDiffs, ",")
	if !strings.Contains(joined, "body_key_removed:messages") || !strings.Contains(joined, "body_key_added:input") {
		t.Fatalf("body diffs = %v", bodyDiffs)
	}
}
