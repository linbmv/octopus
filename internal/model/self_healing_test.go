package model

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestChannelPatchSnapshotNeverContainsKeyMaterial(t *testing.T) {
	channel := &Channel{
		ID: 12, Name: "channel", UserAgent: "client/1", ParamOverride: stringPointer(`{"stream":true}`),
		CustomHeader: []CustomHeader{{HeaderKey: "originator", HeaderValue: "codex"}},
		Keys:         []ChannelKey{{ID: 7, ChannelKey: "secret-key"}},
	}
	snapshot := NewChannelPatchSnapshot(channel)
	encoded := string(mustJSON(snapshot))
	if strings.Contains(encoded, "secret-key") || strings.Contains(encoded, "channel_key") {
		t.Fatalf("patch snapshot contains key material: %s", encoded)
	}
	if !snapshot.Valid() {
		t.Fatal("patch snapshot should be valid")
	}
}

func TestNormalizeDiagnosticHeadersAllowlistsAndBoundsValues(t *testing.T) {
	headers := NormalizeDiagnosticHeaders(http.Header{
		"Authorization": {"Bearer secret"},
		"Content-Type":  {"application/json"},
		"X-Request-Id":  {strings.Repeat("x", 300)},
		"Cookie":        {"session=secret"},
	})
	if _, ok := headers["authorization"]; ok {
		t.Fatal("authorization header was persisted")
	}
	if _, ok := headers["cookie"]; ok {
		t.Fatal("cookie header was persisted")
	}
	if len(headers["x-request-id"][0]) != 259 {
		t.Fatalf("bounded request id length = %d, want 259 including ellipsis", len(headers["x-request-id"][0]))
	}
}

func TestChannelConfigFingerprintChangesPatchableSettingsButNotKeys(t *testing.T) {
	channel := &Channel{ID: 12, Name: "channel", Type: "openai/responses", Model: "model", Keys: []ChannelKey{{ID: 1, ChannelKey: "a"}}}
	first := ChannelConfigFingerprint(channel)
	channel.Keys[0].ChannelKey = "changed-secret"
	if got := ChannelConfigFingerprint(channel); got != first {
		t.Fatal("config fingerprint changed when only key material changed")
	}
	channel.UserAgent = "codex-cli"
	if got := ChannelConfigFingerprint(channel); got == first {
		t.Fatal("config fingerprint did not change for User-Agent change")
	}
}

func TestDiagnosticAttemptNormalizesStoredHeadersAndShapeDiff(t *testing.T) {
	artifact := requestartifact.Build(&httpclient.Request{Body: []byte(`{"model":"m"}`)}, "openai/responses", "m", requestartifact.RewriteSummary{})
	attempt := DiagnosticAttempt{SessionID: "diag", VariantID: "v1", Status: DiagnosticAttemptFailed,
		RequestShape: *artifact, RootCause: RootCauseProtocolDrift,
		ResponseHeaders: map[string][]string{"Authorization": {"secret"}, "Content-Type": {"application/json"}},
		ShapeDiff:       []string{"/input", "/input"}}
	attempt.Normalize()
	// NormalizeStoredHeaders is deliberately generic for records loaded from
	// old databases; validation of incoming HTTP headers uses the allowlist
	// API, so lowercased passthrough is the expected behavior here.
	if values, ok := attempt.ResponseHeaders["authorization"]; !ok || len(values) != 1 {
		t.Fatalf("stored headers = %#v, want lowercased authorization entry", attempt.ResponseHeaders)
	}
	if len(attempt.ShapeDiff) != 1 {
		t.Fatalf("shape diff = %#v, want deduplicated values", attempt.ShapeDiff)
	}
}

func stringPointer(value string) *string { return &value }

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
