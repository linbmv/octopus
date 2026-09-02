package requestrewrite

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxJSONPointerBytes = 512
	MaxJSONPointerDepth = 16
	MaxJSONPointerToken = 128
)

// IsProtectedHeader reports headers whose value can carry upstream
// credentials. Channel rewrite configuration must never set, append, or remove
// these headers; authentication is owned by the provider adapter.
func IsProtectedHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	switch name {
	case "authorization", "proxy-authorization", "authentication",
		"x-api-key", "x-goog-api-key", "api-key", "apikey",
		"token", "x-auth-token", "x-access-token", "access-token",
		"x-amz-security-token", "cookie", "set-cookie", "chatgpt-account-id":
		return true
	}
	return strings.Contains(name, "authorization") ||
		strings.Contains(name, "authentication") ||
		strings.HasSuffix(name, "-api-key") ||
		strings.HasSuffix(name, "-token") ||
		strings.HasSuffix(name, "-credential")
}

// ParseJSONPointer parses the deliberately small JSON Pointer subset used by
// request rewrite rules. The root cannot be replaced, empty tokens and array
// append ("-") are rejected, and only RFC 6901 ~0/~1 escaping is accepted.
func ParseJSONPointer(path string) ([]string, error) {
	if len(path) == 0 || len(path) > MaxJSONPointerBytes {
		return nil, fmt.Errorf("path must contain between 1 and %d bytes", MaxJSONPointerBytes)
	}
	if !utf8.ValidString(path) {
		return nil, fmt.Errorf("path must be valid UTF-8")
	}
	if path[0] != '/' {
		return nil, fmt.Errorf("path must be a JSON Pointer beginning with /")
	}
	rawTokens := strings.Split(path[1:], "/")
	if len(rawTokens) == 0 || len(rawTokens) > MaxJSONPointerDepth {
		return nil, fmt.Errorf("path may contain at most %d segments", MaxJSONPointerDepth)
	}
	tokens := make([]string, len(rawTokens))
	for i, raw := range rawTokens {
		if raw == "" {
			return nil, fmt.Errorf("path segment %d must not be empty", i)
		}
		decoded, err := decodePointerToken(raw)
		if err != nil {
			return nil, fmt.Errorf("path segment %d: %w", i, err)
		}
		if decoded == "-" {
			return nil, fmt.Errorf("array append token - is not supported")
		}
		if len(decoded) > MaxJSONPointerToken {
			return nil, fmt.Errorf("path segment %d exceeds %d bytes", i, MaxJSONPointerToken)
		}
		tokens[i] = decoded
	}
	return tokens, nil
}

func decodePointerToken(token string) (string, error) {
	var b strings.Builder
	b.Grow(len(token))
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			b.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("invalid ~ escape")
		}
		i++
		switch token[i] {
		case '0':
			b.WriteByte('~')
		case '1':
			b.WriteByte('/')
		default:
			return "", fmt.Errorf("invalid ~%c escape", token[i])
		}
	}
	return b.String(), nil
}

// ApplyJSONPointer applies override/remove to an already-decoded JSON value.
// Missing intermediate values and missing remove targets are safe no-ops.
// Override may create the final object member, but never creates intermediate
// containers or grows arrays.
func ApplyJSONPointer(root any, tokens []string, action string, value any) (any, bool, error) {
	if len(tokens) == 0 {
		return root, false, fmt.Errorf("root rewrite is not supported")
	}
	if action != "override" && action != "remove" {
		return root, false, fmt.Errorf("unsupported JSON rewrite action %q", action)
	}
	return applyAt(root, tokens, action, value)
}

func applyAt(node any, tokens []string, action string, value any) (any, bool, error) {
	token := tokens[0]
	if len(tokens) == 1 {
		switch typed := node.(type) {
		case map[string]any:
			if action == "remove" {
				if _, exists := typed[token]; !exists {
					return node, false, nil
				}
				delete(typed, token)
				return typed, true, nil
			}
			typed[token] = value
			return typed, true, nil
		case []any:
			index, ok := parseArrayIndex(token, len(typed))
			if !ok {
				return node, false, nil
			}
			if action == "remove" {
				return append(typed[:index], typed[index+1:]...), true, nil
			}
			typed[index] = value
			return typed, true, nil
		default:
			return node, false, nil
		}
	}

	switch typed := node.(type) {
	case map[string]any:
		child, exists := typed[token]
		if !exists {
			return node, false, nil
		}
		updated, changed, err := applyAt(child, tokens[1:], action, value)
		if err != nil || !changed {
			return node, changed, err
		}
		typed[token] = updated
		return typed, true, nil
	case []any:
		index, ok := parseArrayIndex(token, len(typed))
		if !ok {
			return node, false, nil
		}
		updated, changed, err := applyAt(typed[index], tokens[1:], action, value)
		if err != nil || !changed {
			return node, changed, err
		}
		typed[index] = updated
		return typed, true, nil
	default:
		return node, false, nil
	}
}

func parseArrayIndex(token string, length int) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	for i := range len(token) {
		if token[i] < '0' || token[i] > '9' {
			return 0, false
		}
	}
	index, err := strconv.Atoi(token)
	return index, err == nil && index >= 0 && index < length
}
