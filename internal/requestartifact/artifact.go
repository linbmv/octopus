package requestartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm/httpclient"
)

const (
	// MaxBodyBytes bounds the amount of request content inspected by the
	// artifact builder. The relay already owns the request bytes; this limit
	// prevents the diagnostic representation from retaining an unbounded copy.
	MaxBodyBytes  = 64 << 10
	maxShapeNodes = 256
	maxShapeDepth = 12
)

// Artifact is a redacted description of the final request sent to an upstream
// provider. It deliberately stores shape and allowlisted protocol metadata,
// never credentials or raw user content.
type Artifact struct {
	Method      string            `json:"method,omitempty"`
	URL         string            `json:"url,omitempty"`
	Protocol    string            `json:"protocol,omitempty"`
	Model       string            `json:"model,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        BodyShape         `json:"body"`
	ShapeSHA256 string            `json:"shape_sha256"`
	Rewrite     RewriteSummary    `json:"rewrite"`
}

// BodyShape contains structural JSON information only. Paths use JSON-pointer
// style names; array elements use [] instead of retaining user-controlled
// indexes or values.
type BodyShape struct {
	ContentType  string            `json:"content_type,omitempty"`
	BodyBytes    int               `json:"body_bytes"`
	TopLevelKeys []string          `json:"top_level_keys,omitempty"`
	Paths        map[string]string `json:"paths,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
}

type RewriteSummary struct {
	RawPassthrough        bool `json:"raw_passthrough"`
	ParamOverrideApplied  bool `json:"param_override_applied,omitempty"`
	JSONRewriteApplied    bool `json:"json_rewrite_applied,omitempty"`
	HeaderRewriteApplied  bool `json:"header_rewrite_applied,omitempty"`
	RequestRewriteApplied bool `json:"request_rewrite_applied,omitempty"`
}

// Build creates an artifact from the final outbound request. protocol and
// rewrite describe the selected channel and must not contain secrets.
func Build(request *httpclient.Request, protocol, model string, rewrite RewriteSummary) *Artifact {
	if request == nil {
		return nil
	}

	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" && request.Headers != nil {
		contentType = strings.TrimSpace(request.Headers.Get("Content-Type"))
	}
	body, bodyModel := buildBodyShape(request.Body, contentType)
	if bodyModel != "" {
		model = bodyModel
	}
	rawURL := request.URL
	if strings.TrimSpace(rawURL) == "" {
		rawURL = request.Path
	}
	artifact := &Artifact{
		Method:   strings.ToUpper(strings.TrimSpace(request.Method)),
		URL:      sanitizeURL(rawURL),
		Protocol: strings.TrimSpace(protocol),
		Model:    strings.TrimSpace(model),
		Headers:  captureHeaders(request.Headers),
		Body:     body,
		Rewrite:  rewrite,
	}
	if artifact.Method == "" {
		artifact.Method = "POST"
	}
	artifact.ShapeSHA256 = shapeHash(artifact)
	return artifact
}

func sanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return truncate(raw, 512)
	}
	u.User = nil
	if u.RawQuery != "" {
		query := u.Query()
		for key := range query {
			query[key] = []string{"[redacted]"}
		}
		u.RawQuery = query.Encode()
	}
	return truncate(u.String(), 2048)
}

func captureHeaders(headers map[string][]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	originalByLower := make(map[string]string, len(headers))
	for key := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower != "" {
			originalByLower[lower] = key
		}
	}
	keys := make([]string, 0, len(originalByLower))
	for key := range originalByLower {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		values := headers[originalByLower[key]]
		if isSensitiveHeader(key) {
			result[key] = "[redacted]"
			continue
		}
		if !isAllowlistedHeader(key) {
			result[key] = "[present]"
			continue
		}
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			cleaned = append(cleaned, truncate(strings.TrimSpace(value), 2048))
		}
		result[key] = strings.Join(cleaned, ",")
	}
	return result
}

// isSensitiveHeader delegates to the canonical protected-header list plus the
// cookie pair, so artifact redaction can never lag behind the rewrite gate.
func isSensitiveHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return requestrewrite.IsProtectedHeader(name)
}

func isAllowlistedHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if isSensitiveHeader(name) {
		return false
	}
	switch name {
	case "user-agent", "originator", "anthropic-version", "anthropic-beta",
		"x-codex-beta-features", "x-openai-internal-codex-responses-lite",
		"content-type", "accept", "accept-encoding", "cache-control":
		return true
	default:
		return false
	}
}

// buildBodyShape parses the body once and also returns the top-level "model"
// string so Build never has to unmarshal the same bytes twice.
func buildBodyShape(body []byte, contentType string) (BodyShape, string) {
	shape := BodyShape{ContentType: truncate(contentType, 256), BodyBytes: len(body)}
	if len(body) == 0 {
		return shape, ""
	}
	if len(body) > MaxBodyBytes {
		shape.Truncated = true
		return shape, ""
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		shape.Paths = map[string]string{"/": "non_json"}
		return shape, ""
	}
	shape.Paths = make(map[string]string)
	state := shapeWalker{paths: shape.Paths}
	state.walk("", value, 0)
	model := ""
	if object, ok := value.(map[string]any); ok {
		for key := range object {
			shape.TopLevelKeys = append(shape.TopLevelKeys, key)
		}
		sort.Strings(shape.TopLevelKeys)
		if name, ok := object["model"].(string); ok {
			model = truncate(strings.TrimSpace(name), 256)
		}
	}
	return shape, model
}

type shapeWalker struct {
	paths map[string]string
	nodes int
}

func (w *shapeWalker) walk(path string, value any, depth int) {
	if w.nodes >= maxShapeNodes || depth > maxShapeDepth {
		return
	}
	w.nodes++
	switch typed := value.(type) {
	case map[string]any:
		w.paths[pathOrRoot(path)] = "object"
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			w.walk(path+"/"+escapePointerToken(key), typed[key], depth+1)
		}
	case []any:
		w.paths[pathOrRoot(path)] = "array"
		w.paths[pathOrRoot(path)+"#length"] = lengthBucket(len(typed))
		if len(typed) > 0 {
			w.walk(path+"/[]", typed[0], depth+1)
		}
	case string:
		w.paths[pathOrRoot(path)] = "string"
	case bool:
		w.paths[pathOrRoot(path)] = "boolean"
	case float64, json.Number:
		w.paths[pathOrRoot(path)] = "number"
	case nil:
		w.paths[pathOrRoot(path)] = "null"
	default:
		w.paths[pathOrRoot(path)] = "other"
	}
}

func shapeHash(artifact *Artifact) string {
	canonical := struct {
		Method   string
		URL      string
		Protocol string
		Model    string
		Headers  map[string]string
		Body     struct {
			ContentType  string
			TopLevelKeys []string
			Paths        map[string]string
			Truncated    bool
		}
		Rewrite RewriteSummary
	}{
		Method: artifact.Method, URL: artifact.URL, Protocol: artifact.Protocol,
		Model: artifact.Model, Headers: artifact.Headers, Rewrite: artifact.Rewrite,
	}
	canonical.Body.ContentType = artifact.Body.ContentType
	canonical.Body.TopLevelKeys = artifact.Body.TopLevelKeys
	canonical.Body.Paths = artifact.Body.Paths
	canonical.Body.Truncated = artifact.Body.Truncated
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func pathOrRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func escapePointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func lengthBucket(length int) string {
	switch {
	case length == 0:
		return "0"
	case length == 1:
		return "1"
	case length <= 4:
		return "2-4"
	case length <= 16:
		return "5-16"
	default:
		return "17+"
	}
}

func truncate(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}
