// Package importer parses redacted golden samples for P2 self-healing compare mode.
//
// Samples may come from cURL, HAR, or compact JSON. Authentication material is
// rejected or redacted before any persistence path is allowed to see the value.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/requestrewrite"
)

const (
	maxSampleBodyBytes   = 64 * 1024
	maxSampleHeaderCount = 64
	maxSampleHeaderBytes = 4 * 1024
)

var (
	ErrSampleTooLarge     = errors.New("golden sample exceeds size limits")
	ErrSampleAuthSecret   = errors.New("golden sample contains authentication secrets")
	ErrSampleInvalidURL   = errors.New("golden sample URL is not allowed")
	ErrSampleInvalidJSON  = errors.New("golden sample body is not valid JSON")
	ErrSampleEmpty        = errors.New("golden sample is empty")
	ErrSampleUnsupported  = errors.New("golden sample format is unsupported")
)

// Sample is a fully redacted, SSRF-safe golden request used only for shape diff.
type Sample struct {
	Source     string              `json:"source"`
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Host       string              `json:"host"`
	Path       string              `json:"path"`
	Headers    map[string][]string `json:"headers"`
	BodyShape  map[string]any      `json:"body_shape,omitempty"`
	BodyKeys   []string            `json:"body_keys,omitempty"`
	RawBodyLen int                 `json:"raw_body_len,omitempty"`
}

// ParseJSONSample accepts a compact JSON object:
//
//	{"method":"POST","url":"https://example.com/v1/...","headers":{"User-Agent":["..."]},"body":{...}}
//
// Authentication headers are rejected. Bodies larger than 64 KiB are rejected.
func ParseJSONSample(raw []byte) (*Sample, error) {
	if len(bytesTrimSpace(raw)) == 0 {
		return nil, ErrSampleEmpty
	}
	if len(raw) > maxSampleBodyBytes+maxSampleHeaderBytes {
		return nil, ErrSampleTooLarge
	}
	var payload struct {
		Method  string              `json:"method"`
		URL     string              `json:"url"`
		Headers map[string][]string `json:"headers"`
		Body    json.RawMessage     `json:"body"`
		Source  string              `json:"source"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSampleInvalidJSON, err)
	}
	method := strings.ToUpper(strings.TrimSpace(payload.Method))
	if method == "" {
		method = "POST"
	}
	parsedURL, err := validateSampleURL(payload.URL)
	if err != nil {
		return nil, err
	}
	headers, err := sanitizeHeaders(payload.Headers)
	if err != nil {
		return nil, err
	}
	sample := &Sample{
		Source:  firstNonEmpty(payload.Source, "json"),
		Method:  method,
		URL:     parsedURL.String(),
		Host:    parsedURL.Host,
		Path:    parsedURL.Path,
		Headers: headers,
	}
	if len(payload.Body) > 0 {
		if len(payload.Body) > maxSampleBodyBytes {
			return nil, ErrSampleTooLarge
		}
		shape, keys, err := bodyShape(payload.Body)
		if err != nil {
			return nil, err
		}
		sample.BodyShape = shape
		sample.BodyKeys = keys
		sample.RawBodyLen = len(payload.Body)
	}
	return sample, nil
}

// DiffHeaders returns public header names present only in left, only in right,
// or with different redacted values. Authentication names never appear.
func DiffHeaders(left, right map[string][]string) []string {
	names := map[string]struct{}{}
	for name := range left {
		names[strings.ToLower(name)] = struct{}{}
	}
	for name := range right {
		names[strings.ToLower(name)] = struct{}{}
	}
	var diffs []string
	for name := range names {
		if isAuthHeader(name) {
			continue
		}
		lv := headerValuesCI(left, name)
		rv := headerValuesCI(right, name)
		switch {
		case len(lv) == 0 && len(rv) > 0:
			diffs = append(diffs, "header_added:"+name)
		case len(lv) > 0 && len(rv) == 0:
			diffs = append(diffs, "header_removed:"+name)
		case !equalStringSlices(lv, rv):
			diffs = append(diffs, "header_changed:"+name)
		}
	}
	return diffs
}

func headerValuesCI(headers map[string][]string, name string) []string {
	if values, ok := headers[name]; ok {
		return normalizeHeaderValues(values)
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return normalizeHeaderValues(values)
		}
	}
	return nil
}

// DiffBodyKeys reports top-level JSON keys present only on one side.
func DiffBodyKeys(left, right []string) []string {
	set := func(keys []string) map[string]struct{} {
		out := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = struct{}{}
			}
		}
		return out
	}
	lset, rset := set(left), set(right)
	var diffs []string
	for key := range lset {
		if _, ok := rset[key]; !ok {
			diffs = append(diffs, "body_key_removed:"+key)
		}
	}
	for key := range rset {
		if _, ok := lset[key]; !ok {
			diffs = append(diffs, "body_key_added:"+key)
		}
	}
	return diffs
}

func validateSampleURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrSampleInvalidURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, ErrSampleInvalidURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return nil, ErrSampleInvalidURL
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return nil, ErrSampleInvalidURL
	}
	// Block obvious link-local / loopback forms used for SSRF. Broader private
	// network rejection belongs to the network dialer; this only keeps sample
	// import from accepting clearly unsafe targets.
	if host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" || strings.HasPrefix(host, "169.254.") {
		return nil, ErrSampleInvalidURL
	}
	// Drop userinfo so credentials never survive in sample.URL.
	parsed.User = nil
	return parsed, nil
}

func sanitizeHeaders(headers map[string][]string) (map[string][]string, error) {
	if len(headers) > maxSampleHeaderCount {
		return nil, ErrSampleTooLarge
	}
	out := make(map[string][]string, len(headers))
	total := 0
	for name, values := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if isAuthHeader(name) {
			return nil, fmt.Errorf("%w: %s", ErrSampleAuthSecret, name)
		}
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			if looksLikeSecret(value) {
				return nil, fmt.Errorf("%w: %s", ErrSampleAuthSecret, name)
			}
			total += utf8.RuneCountInString(name) + utf8.RuneCountInString(value)
			if total > maxSampleHeaderBytes {
				return nil, ErrSampleTooLarge
			}
			cleaned = append(cleaned, value)
		}
		out[name] = cleaned
	}
	return out, nil
}

func bodyShape(raw json.RawMessage) (map[string]any, []string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrSampleInvalidJSON, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return map[string]any{"_type": typeName(value)}, nil, nil
	}
	shape := make(map[string]any, len(object))
	keys := make([]string, 0, len(object))
	for key, child := range object {
		keys = append(keys, key)
		shape[key] = typeName(child)
	}
	return shape, keys, nil
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// isAuthHeader delegates to the canonical protected-header list so the
// importer's redaction gate never lags behind the rewrite allowlist.
func isAuthHeader(name string) bool {
	return requestrewrite.IsProtectedHeader(name)
}

func looksLikeSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ") {
		return true
	}
	if strings.HasPrefix(trimmed, "sk-") || strings.HasPrefix(trimmed, "sk-ant-") {
		return true
	}
	return false
}

func normalizeHeaderValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
