package jsonpatch

import (
	"encoding/json"
	"strconv"
)

func PatchModel(raw []byte, model string) ([]byte, bool) {
	if model == "" || !json.Valid(raw) {
		return raw, false
	}

	i := skipWhitespace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return raw, false
	}
	i++

	for {
		i = skipWhitespace(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			return raw, false
		}
		if raw[i] != '"' {
			return raw, false
		}

		keyStart := i
		keyEnd, ok := scanString(raw, i)
		if !ok {
			return raw, false
		}
		key, err := strconv.Unquote(string(raw[keyStart:keyEnd]))
		if err != nil {
			return raw, false
		}

		i = skipWhitespace(raw, keyEnd)
		if i >= len(raw) || raw[i] != ':' {
			return raw, false
		}
		i = skipWhitespace(raw, i+1)
		valueStart := i
		valueEnd, ok := scanValue(raw, valueStart)
		if !ok {
			return raw, false
		}

		if key == "model" {
			return patchStringValue(raw, valueStart, valueEnd, model)
		}

		i = skipWhitespace(raw, valueEnd)
		if i >= len(raw) {
			return raw, false
		}
		switch raw[i] {
		case ',':
			i++
		case '}':
			return raw, false
		default:
			return raw, false
		}
	}
}

// TopLevelModel 返回顶层 JSON 对象的 model 字段值，仅当它确实是顶层字符串且恰好出现一次时返回 ok=true。
// raw passthrough 用它做安全门槛：读不到顶层 string model、或顶层出现重复 model 时回退到常规转换路径，避免发送错误模型。
func TopLevelModel(raw []byte) (string, bool) {
	if !json.Valid(raw) {
		return "", false
	}

	i := skipWhitespace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return "", false
	}
	i++

	var model string
	found := false

	for {
		i = skipWhitespace(raw, i)
		if i >= len(raw) {
			return "", false
		}
		if raw[i] == '}' {
			// 扫描完整个顶层对象后才返回：确保 model 恰好出现一次。
			if found {
				return model, true
			}
			return "", false
		}
		if raw[i] != '"' {
			return "", false
		}

		keyStart := i
		keyEnd, ok := scanString(raw, i)
		if !ok {
			return "", false
		}
		key, err := strconv.Unquote(string(raw[keyStart:keyEnd]))
		if err != nil {
			return "", false
		}

		i = skipWhitespace(raw, keyEnd)
		if i >= len(raw) || raw[i] != ':' {
			return "", false
		}
		valueStart := skipWhitespace(raw, i+1)
		valueEnd, ok := scanValue(raw, valueStart)
		if !ok {
			return "", false
		}

		if key == "model" {
			if found {
				// 顶层出现重复 model：上游 JSON 解析通常按最后一个生效，与本函数按首个 patch 的行为不一致，
				// 存在模型路由绕过风险，直接判失败让上层回退到常规转换路径。
				return "", false
			}
			if raw[valueStart] != '"' {
				return "", false
			}
			value, err := strconv.Unquote(string(raw[valueStart:valueEnd]))
			if err != nil {
				return "", false
			}
			model = value
			found = true
		}

		i = skipWhitespace(raw, valueEnd)
		if i >= len(raw) {
			return "", false
		}
		switch raw[i] {
		case ',':
			i++
		case '}':
			// 末尾字段后直接收口对象：同样要求 model 已恰好出现一次。
			if found {
				return model, true
			}
			return "", false
		default:
			return "", false
		}
	}
}

func EnsureStreamIncludeUsage(raw []byte) ([]byte, bool) {
	if !json.Valid(raw) {
		return raw, false
	}

	i := skipWhitespace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return raw, false
	}

	objectEnd, ok := scanComposite(raw, i, '{', '}')
	if !ok {
		return raw, false
	}

	i++
	for {
		i = skipWhitespace(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			return appendObjectField(raw, objectEnd, []byte(`"stream_options":{"include_usage":true}`)), true
		}
		if raw[i] != '"' {
			return raw, false
		}

		keyStart := i
		keyEnd, ok := scanString(raw, i)
		if !ok {
			return raw, false
		}
		key, err := strconv.Unquote(string(raw[keyStart:keyEnd]))
		if err != nil {
			return raw, false
		}

		i = skipWhitespace(raw, keyEnd)
		if i >= len(raw) || raw[i] != ':' {
			return raw, false
		}
		valueStart := skipWhitespace(raw, i+1)
		valueEnd, ok := scanValue(raw, valueStart)
		if !ok {
			return raw, false
		}

		if key == "stream_options" {
			return ensureIncludeUsageInObject(raw, valueStart, valueEnd)
		}

		i = skipWhitespace(raw, valueEnd)
		if i >= len(raw) {
			return raw, false
		}
		switch raw[i] {
		case ',':
			i++
		case '}':
			return appendObjectField(raw, objectEnd, []byte(`"stream_options":{"include_usage":true}`)), true
		default:
			return raw, false
		}
	}
}

func ensureIncludeUsageInObject(raw []byte, start, end int) ([]byte, bool) {
	if start >= end || raw[start] != '{' {
		return raw, false
	}

	i := start + 1
	for {
		i = skipWhitespace(raw, i)
		if i >= end || raw[i] == '}' {
			return appendObjectField(raw, end, []byte(`"include_usage":true`)), true
		}
		if raw[i] != '"' {
			return raw, false
		}

		keyStart := i
		keyEnd, ok := scanString(raw, i)
		if !ok {
			return raw, false
		}
		key, err := strconv.Unquote(string(raw[keyStart:keyEnd]))
		if err != nil {
			return raw, false
		}

		i = skipWhitespace(raw, keyEnd)
		if i >= end || raw[i] != ':' {
			return raw, false
		}
		valueStart := skipWhitespace(raw, i+1)
		valueEnd, ok := scanValue(raw, valueStart)
		if !ok || valueEnd > end {
			return raw, false
		}

		if key == "include_usage" {
			value := string(raw[valueStart:valueEnd])
			switch value {
			case "true":
				return raw, false
			case "false":
				patched := make([]byte, 0, len(raw)-len("false")+len("true"))
				patched = append(patched, raw[:valueStart]...)
				patched = append(patched, []byte("true")...)
				patched = append(patched, raw[valueEnd:]...)
				return patched, true
			default:
				return raw, false
			}
		}

		i = skipWhitespace(raw, valueEnd)
		if i >= end {
			return raw, false
		}
		switch raw[i] {
		case ',':
			i++
		case '}':
			return appendObjectField(raw, i+1, []byte(`"include_usage":true`)), true
		default:
			return raw, false
		}
	}
}

func appendObjectField(raw []byte, objectEnd int, field []byte) []byte {
	insertAt := trimRightWhitespace(raw, 0, objectEnd-1)
	prefix := raw[:insertAt]
	suffix := raw[insertAt:]
	separator := []byte(nil)
	if !objectHasNoFields(prefix) {
		separator = []byte(",")
	}

	patched := make([]byte, 0, len(raw)+len(separator)+len(field))
	patched = append(patched, prefix...)
	patched = append(patched, separator...)
	patched = append(patched, field...)
	patched = append(patched, suffix...)
	return patched
}

func objectHasNoFields(prefix []byte) bool {
	for i := len(prefix) - 1; i >= 0; i-- {
		switch prefix[i] {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return prefix[i] == '{'
		}
	}
	return true
}

func patchStringValue(raw []byte, start, end int, model string) ([]byte, bool) {
	if start >= len(raw) || raw[start] != '"' {
		return raw, false
	}

	oldModel, err := strconv.Unquote(string(raw[start:end]))
	if err != nil || oldModel == model {
		return raw, false
	}

	quoted := []byte(strconv.Quote(model))
	patched := make([]byte, 0, len(raw)-(end-start)+len(quoted))
	patched = append(patched, raw[:start]...)
	patched = append(patched, quoted...)
	patched = append(patched, raw[end:]...)
	return patched, true
}

func skipWhitespace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}

func scanValue(raw []byte, i int) (int, bool) {
	i = skipWhitespace(raw, i)
	if i >= len(raw) {
		return i, false
	}

	switch raw[i] {
	case '"':
		return scanString(raw, i)
	case '{':
		return scanComposite(raw, i, '{', '}')
	case '[':
		return scanComposite(raw, i, '[', ']')
	default:
		return scanScalar(raw, i)
	}
}

func scanString(raw []byte, i int) (int, bool) {
	if i >= len(raw) || raw[i] != '"' {
		return i, false
	}
	i++
	for i < len(raw) {
		switch raw[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return i, false
}

func scanComposite(raw []byte, i int, open, close byte) (int, bool) {
	if i >= len(raw) || raw[i] != open {
		return i, false
	}
	depth := 0
	for i < len(raw) {
		switch raw[i] {
		case '"':
			end, ok := scanString(raw, i)
			if !ok {
				return i, false
			}
			i = end
			continue
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
		i++
	}
	return i, false
}

func scanScalar(raw []byte, i int) (int, bool) {
	start := i
	for i < len(raw) {
		switch raw[i] {
		case ',', '}', ']':
			end := trimRightWhitespace(raw, start, i)
			return end, end > start
		default:
			i++
		}
	}
	end := trimRightWhitespace(raw, start, i)
	return end, end > start
}

func trimRightWhitespace(raw []byte, start, end int) int {
	for end > start {
		switch raw[end-1] {
		case ' ', '\n', '\r', '\t':
			end--
		default:
			return end
		}
	}
	return end
}
