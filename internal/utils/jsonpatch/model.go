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
