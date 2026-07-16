package op

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
)

const (
	relayLogRedactedValue    = "[REDACTED]"
	relayLogRedactedURLValue = "[REDACTED_URL]"
)

var (
	relayLogURLPattern                 = regexp.MustCompile(`(?i)\b(?:https?|wss?|socks5?)://[^\s"'<>]+`)
	relayLogBearerPattern              = regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]+`)
	relayLogSensitiveHeaderPattern     = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|x-api-key|api-key|x-goog-api-key|ocp-apim-subscription-key|cookie|set-cookie)\s*:\s*)[^\r\n]+`)
	relayLogSensitiveAssignmentPattern = regexp.MustCompile(`(?i)("?(?:authorization|proxy[-_]?authorization|x[-_]?api[-_]?key|api[-_]?key|x[-_]?goog[-_]?api[-_]?key|ocp[-_]?apim[-_]?subscription[-_]?key|access[-_]?token|refresh[-_]?token|id[-_]?token|client[-_]?secret|secret[-_]?access[-_]?key|password|passwd|credential|credentials|cookie|set[-_]?cookie)"?)(\s*[:=]\s*)(Bearer[ \t]+[A-Za-z0-9._~+/=-]+|"[^"]*"|'[^']*'|[^\s,;}\]]+)`)
)

// RelayLogContentModeGet returns the hot-reloaded content policy from the
// settings cache. Missing or invalid values are errors so callers fail closed
// instead of accidentally persisting request bodies.
func RelayLogContentModeGet() (model.RelayLogContentMode, error) {
	value, err := SettingGetString(model.SettingKeyRelayLogContentMode)
	if err != nil {
		return "", fmt.Errorf("get relay log content mode: %w", err)
	}
	mode, err := model.ParseRelayLogContentMode(value)
	if err != nil {
		return "", fmt.Errorf("invalid relay log content mode: %w", err)
	}
	return mode, nil
}

func applyRelayLogContentPolicy(relayLog model.RelayLog, mode model.RelayLogContentMode) model.RelayLog {
	// Error and attempt messages remain available in metadata mode for
	// diagnostics, but URL credentials, auth headers, and credential-shaped
	// fields must never reach memory cache, SSE, or the database.
	relayLog.Error = truncateRelayLogContent(sanitizeRelayLogText(relayLog.Error))
	for i := range relayLog.Attempts {
		relayLog.Attempts[i].Msg = truncateRelayLogContent(sanitizeRelayLogText(relayLog.Attempts[i].Msg))
	}

	switch mode {
	case model.RelayLogContentModeFull:
		relayLog.RequestContent = truncateRelayLogContent(sanitizeRelayLogContent(relayLog.RequestContent))
		relayLog.ResponseContent = truncateRelayLogContent(sanitizeRelayLogContent(relayLog.ResponseContent))
	default:
		// metadata is the secure default. disabled is handled before this
		// function, but clearing here keeps the helper fail-safe.
		relayLog.RequestContent = ""
		relayLog.ResponseContent = ""
	}

	return relayLog
}

func truncateRelayLogContent(content string) string {
	content = strings.ToValidUTF8(content, "\uFFFD")
	if len(content) <= conf.MaxRelayLogContentBytes {
		return content
	}
	suffix := "\n[truncated]"
	limit := conf.MaxRelayLogContentBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(content[:limit]) {
		limit--
	}
	return content[:limit] + suffix
}

func sanitizeRelayLogContent(content string) string {
	if content == "" {
		return ""
	}
	if redacted, ok := sanitizeRelayLogJSON(content); ok {
		return sanitizeRelayLogFreeText(redacted)
	}

	// Streaming responses commonly contain one JSON object per `data:` line.
	// Redact each JSON payload before applying the plaintext fallback.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if redacted, ok := sanitizeRelayLogJSON(payload); ok {
			indent := line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
			lines[i] = indent + "data: " + redacted
		}
	}
	return sanitizeRelayLogText(strings.Join(lines, "\n"))
}

func sanitizeRelayLogJSON(content string) (string, bool) {
	if !json.Valid([]byte(content)) {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	redactRelayLogJSONValue(value)
	redacted, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(redacted), true
}

func redactRelayLogJSONValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if relayLogFieldIsSensitive(key) {
				typed[key] = relayLogRedactedValue
				continue
			}
			redactRelayLogJSONValue(child)
		}
	case []any:
		for _, child := range typed {
			redactRelayLogJSONValue(child)
		}
	}
}

func relayLogFieldIsSensitive(key string) bool {
	field := normalizeRelayLogFieldName(key)
	switch field {
	case "authorization", "proxyauthorization", "apikey", "xapikey",
		"accesskey", "accesskeyid", "secretaccesskey", "accesstoken",
		"refreshtoken", "idtoken", "apitoken", "authtoken", "bearertoken", "token",
		"xgoogapikey", "ocpapimsubscriptionkey", "subscriptionkey", "clientsecret", "secret",
		"password", "passwd", "credential", "credentials", "cookie", "setcookie",
		"key", "auth", "sig", "signature":
		return true
	default:
		return strings.HasSuffix(field, "apikey") || strings.HasSuffix(field, "apikeyid") ||
			strings.HasSuffix(field, "accesstoken") || strings.HasSuffix(field, "refreshtoken") ||
			strings.HasSuffix(field, "authtoken") || strings.HasSuffix(field, "clientsecret") ||
			strings.HasSuffix(field, "password") || strings.HasSuffix(field, "passwd") ||
			strings.HasSuffix(field, "credential")
	}
}

func normalizeRelayLogFieldName(key string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(key) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func relayLogURLQueryFieldIsSensitive(key string) bool {
	if relayLogFieldIsSensitive(key) {
		return true
	}
	switch normalizeRelayLogFieldName(key) {
	case "key", "auth", "sig", "signature":
		return true
	default:
		return false
	}
}

func sanitizeRelayLogText(content string) string {
	if content == "" {
		return ""
	}
	content = relayLogSensitiveHeaderPattern.ReplaceAllString(content, `${1}`+relayLogRedactedValue)
	content = relayLogSensitiveAssignmentPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := relayLogSensitiveAssignmentPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return relayLogRedactedValue
		}
		value := relayLogRedactedValue
		if strings.HasPrefix(parts[3], `"`) {
			value = `"` + relayLogRedactedValue + `"`
		} else if strings.HasPrefix(parts[3], `'`) {
			value = `'` + relayLogRedactedValue + `'`
		}
		return parts[1] + parts[2] + value
	})
	return sanitizeRelayLogFreeText(content)
}

func sanitizeRelayLogFreeText(content string) string {
	content = relayLogURLPattern.ReplaceAllStringFunc(content, sanitizeRelayLogURL)
	content = relayLogBearerPattern.ReplaceAllString(content, "Bearer "+relayLogRedactedValue)
	return content
}

func sanitizeRelayLogURL(raw string) string {
	core := strings.TrimRight(raw, ".,;:!?)")
	suffix := raw[len(core):]
	parsed, err := url.Parse(core)
	if err != nil || parsed.Host == "" {
		return relayLogRedactedURLValue + suffix
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		if relayLogURLQueryFieldIsSensitive(key) {
			query.Set(key, relayLogRedactedValue)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String() + suffix
}
