package relay

import (
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// relayAttachment is deliberately local to the relay. AxonHub's common model
// can represent documents, but not every wire protocol has a common place for
// file_id/file_url. Keeping these source fields until the outbound boundary
// lets OpenAI-compatible channels preserve them without leaking them into the
// normal request log or database model.
type relayAttachment struct {
	Data     string
	URL      string
	ID       string
	Filename string
	MIMEType string
}

type relayAttachmentTurn struct {
	Role        string
	RoleOrdinal int
	Files       []relayAttachment
}

// applyRelayAttachmentCompatibility restores file content that the common
// transformer cannot model yet (notably OpenAI file/input_file parts). It is
// intentionally performed after the normal outbound transformer so text,
// tools, reasoning, and provider-specific fields keep their existing behavior.
func (ra *relayAttempt) applyRelayAttachmentCompatibility(outboundRequest *httpclient.Request) error {
	if ra == nil || ra.internalRequest == nil || ra.internalRequest.RawRequest == nil || ra.channel == nil || outboundRequest == nil {
		return nil
	}
	if !isJSONRelayRequest(outboundRequest) {
		return nil
	}

	turns := collectRelayAttachmentTurns(ra.internalRequest.APIFormat, ra.internalRequest.RawRequest.Body)
	if len(turns) == 0 {
		return nil
	}

	body, changed, err := patchRelayAttachmentBody(outboundRequest.Body, ra.channel.Type, turns)
	if err != nil {
		return fmt.Errorf("patch outbound attachments: %w", err)
	}
	if changed {
		outboundRequest.Body = body
	}
	return nil
}

func isJSONRelayRequest(request *httpclient.Request) bool {
	if request == nil {
		return false
	}
	return strings.Contains(strings.ToLower(request.ContentType+" "+request.Headers.Get("Content-Type")), "application/json")
}

func collectRelayAttachmentTurns(format llm.APIFormat, body []byte) []relayAttachmentTurn {
	if len(body) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}

	switch format {
	case llm.APIFormatOpenAIChatCompletion, llm.APIFormatAnthropicMessage:
		return collectMessageAttachmentTurns(root["messages"])
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		return collectResponsesAttachmentTurns(root["input"])
	case llm.APIFormatGeminiContents:
		return collectGeminiAttachmentTurns(root["contents"])
	default:
		return nil
	}
}

func collectMessageAttachmentTurns(raw any) []relayAttachmentTurn {
	messages, ok := raw.([]any)
	if !ok {
		return nil
	}
	roleOrdinals := make(map[string]int)
	turns := make([]relayAttachmentTurn, 0)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeAttachmentRole(stringValue(message["role"]))
		ordinal := roleOrdinals[role]
		roleOrdinals[role] = ordinal + 1
		files := collectAttachmentParts(message["content"])
		if len(files) > 0 {
			turns = append(turns, relayAttachmentTurn{Role: role, RoleOrdinal: ordinal, Files: files})
		}
	}
	return turns
}

func collectResponsesAttachmentTurns(raw any) []relayAttachmentTurn {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	roleOrdinals := make(map[string]int)
	turns := make([]relayAttachmentTurn, 0)
	var syntheticUser *relayAttachmentTurn
	flushSynthetic := func() {
		if syntheticUser != nil && len(syntheticUser.Files) > 0 {
			turns = append(turns, *syntheticUser)
		}
		syntheticUser = nil
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
		if typ == "message" {
			flushSynthetic()
			role := normalizeAttachmentRole(stringValue(item["role"]))
			ordinal := roleOrdinals[role]
			roleOrdinals[role] = ordinal + 1
			if files := collectAttachmentParts(item["content"]); len(files) > 0 {
				turns = append(turns, relayAttachmentTurn{Role: role, RoleOrdinal: ordinal, Files: files})
			}
			continue
		}
		if attachment := attachmentFromPart(item); attachment != nil {
			if syntheticUser == nil {
				ordinal := roleOrdinals["user"]
				roleOrdinals["user"] = ordinal + 1
				syntheticUser = &relayAttachmentTurn{Role: "user", RoleOrdinal: ordinal}
			}
			syntheticUser.Files = append(syntheticUser.Files, *attachment)
		}
	}
	flushSynthetic()
	return turns
}

func collectGeminiAttachmentTurns(raw any) []relayAttachmentTurn {
	contents, ok := raw.([]any)
	if !ok {
		return nil
	}
	roleOrdinals := make(map[string]int)
	turns := make([]relayAttachmentTurn, 0)
	for _, rawContent := range contents {
		content, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeAttachmentRole(stringValue(content["role"]))
		if role == "assistant" {
			role = "model"
		}
		ordinal := roleOrdinals[role]
		roleOrdinals[role] = ordinal + 1
		if files := collectAttachmentParts(content["parts"]); len(files) > 0 {
			turns = append(turns, relayAttachmentTurn{Role: role, RoleOrdinal: ordinal, Files: files})
		}
	}
	return turns
}

func collectAttachmentParts(raw any) []relayAttachment {
	parts, ok := raw.([]any)
	if !ok {
		return nil
	}
	files := make([]relayAttachment, 0)
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		if attachment := attachmentFromPart(part); attachment != nil {
			files = append(files, *attachment)
		}
	}
	return files
}

func attachmentFromPart(part map[string]any) *relayAttachment {
	if part == nil {
		return nil
	}
	typ := strings.ToLower(strings.TrimSpace(stringValue(part["type"])))
	if typ != "file" && typ != "input_file" && typ != "document" && typ != "file_data" {
		// Gemini's parts are identified by inlineData/fileData rather than type.
		if part["inlineData"] == nil && part["inline_data"] == nil && part["fileData"] == nil && part["file_data"] == nil {
			return nil
		}
	}

	attachment := &relayAttachment{
		Filename: firstString(part, "filename", "file_name", "name"),
		MIMEType: firstString(part, "mime_type", "mimeType", "media_type"),
		Data:     firstString(part, "file_data", "fileData", "data"),
		URL:      firstString(part, "file_url", "fileUri", "file_uri", "url"),
		ID:       firstString(part, "file_id", "fileId"),
	}

	for _, key := range []string{"file", "source", "inlineData", "inline_data", "fileData", "file_data"} {
		nested, ok := part[key].(map[string]any)
		if !ok {
			continue
		}
		if attachment.Filename == "" {
			attachment.Filename = firstString(nested, "filename", "file_name", "name")
		}
		if attachment.MIMEType == "" {
			attachment.MIMEType = firstString(nested, "mime_type", "mimeType", "media_type")
		}
		if attachment.Data == "" {
			attachment.Data = firstString(nested, "file_data", "data")
		}
		if attachment.URL == "" {
			attachment.URL = firstString(nested, "file_url", "fileUri", "file_uri", "url")
		}
		if attachment.ID == "" {
			attachment.ID = firstString(nested, "file_id", "fileId")
		}
		if typ == "" {
			switch key {
			case "inlineData", "inline_data":
				attachment.MIMEType = firstString(nested, "mime_type", "mimeType", "media_type")
			case "fileData", "file_data":
				attachment.URL = firstString(nested, "fileUri", "file_uri", "url")
			}
		}
	}

	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.Data)), "http://") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.Data)), "https://") {
		if attachment.URL == "" {
			attachment.URL = attachment.Data
		}
		attachment.Data = ""
	}
	if attachment.Data == "" && attachment.URL == "" && attachment.ID == "" {
		return nil
	}
	return attachment
}

func patchRelayAttachmentBody(body []byte, target llm.APIFormat, turns []relayAttachmentTurn) ([]byte, bool, error) {
	if len(turns) == 0 {
		return body, false, nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, false, fmt.Errorf("decode outbound JSON: %w", err)
	}
	changed := false
	switch target {
	case llm.APIFormatOpenAIChatCompletion:
		changed = patchOpenAIChatAttachments(root, turns)
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		changed = patchOpenAIResponsesAttachments(root, turns)
	case llm.APIFormatAnthropicMessage:
		changed = patchAnthropicAttachments(root, turns)
	case llm.APIFormatGeminiContents:
		changed = patchGeminiAttachments(root, turns)
	default:
		return body, false, nil
	}
	if !changed {
		return body, false, nil
	}
	patched, err := json.Marshal(root)
	if err != nil {
		return body, false, fmt.Errorf("encode outbound JSON: %w", err)
	}
	return patched, true, nil
}

func patchOpenAIChatAttachments(root map[string]any, turns []relayAttachmentTurn) bool {
	messages, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, turn := range turns {
		message := findRelayMessage(messages, turn.Role, turn.RoleOrdinal)
		if message == nil {
			continue
		}
		content := ensureJSONArray(message, "content", func(value any) any {
			if text, ok := value.(string); ok {
				return map[string]any{"type": "text", "text": text}
			}
			return nil
		})
		for _, attachment := range turn.Files {
			part := openAIChatAttachmentPart(attachment)
			if relayAttachmentAlreadyPresent(content, attachment) {
				continue
			}
			content = append(content, part)
			changed = true
		}
		message["content"] = content
	}
	return changed
}

func patchOpenAIResponsesAttachments(root map[string]any, turns []relayAttachmentTurn) bool {
	input, ok := root["input"].([]any)
	if !ok {
		if text, isText := root["input"].(string); isText {
			input = []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
		} else {
			input = []any{}
		}
		root["input"] = input
	}
	changed := false
	for _, turn := range turns {
		message := findRelayResponsesMessage(input, turn.Role, turn.RoleOrdinal)
		if message == nil {
			message = map[string]any{"type": "message", "role": turn.Role, "content": []any{}}
			input = append(input, message)
			root["input"] = input
		}
		content := ensureJSONArray(message, "content", func(value any) any {
			if text, ok := value.(string); ok {
				return map[string]any{"type": "input_text", "text": text}
			}
			return nil
		})
		for _, attachment := range turn.Files {
			// Responses' raw-input replay can keep an unrepresented top-level
			// input_file. Check the whole input before adding a second copy to
			// the normalized message content.
			if relayAttachmentAlreadyPresent(input, attachment) || relayAttachmentAlreadyPresent(content, attachment) {
				continue
			}
			content = append(content, openAIResponsesAttachmentPart(attachment))
			changed = true
		}
		message["content"] = content
	}
	return changed
}

func patchAnthropicAttachments(root map[string]any, turns []relayAttachmentTurn) bool {
	messages, ok := root["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, turn := range turns {
		message := findRelayMessage(messages, turn.Role, turn.RoleOrdinal)
		if message == nil {
			continue
		}
		content := ensureJSONArray(message, "content", func(value any) any {
			if text, ok := value.(string); ok {
				return map[string]any{"type": "text", "text": text}
			}
			return nil
		})
		for _, attachment := range turn.Files {
			if relayAttachmentAlreadyPresent(content, attachment) {
				continue
			}
			if part, ok := anthropicAttachmentPart(attachment); ok {
				content = append(content, part)
				changed = true
			}
		}
		message["content"] = content
	}
	return changed
}

func patchGeminiAttachments(root map[string]any, turns []relayAttachmentTurn) bool {
	contents, ok := root["contents"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, turn := range turns {
		role := turn.Role
		if role == "assistant" {
			role = "model"
		}
		content := findRelayMessage(contents, role, turn.RoleOrdinal)
		if content == nil {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, attachment := range turn.Files {
			if relayAttachmentAlreadyPresent(parts, attachment) {
				continue
			}
			if part, ok := geminiAttachmentPart(attachment); ok {
				parts = append(parts, part)
				changed = true
			}
		}
		content["parts"] = parts
	}
	return changed
}

func findRelayMessage(messages []any, role string, roleOrdinal int) map[string]any {
	seen := 0
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || normalizeAttachmentRole(stringValue(message["role"])) != role {
			continue
		}
		if seen == roleOrdinal {
			return message
		}
		seen++
	}
	return nil
}

func findRelayResponsesMessage(items []any, role string, roleOrdinal int) map[string]any {
	seen := 0
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || strings.ToLower(strings.TrimSpace(stringValue(item["type"]))) != "message" || normalizeAttachmentRole(stringValue(item["role"])) != role {
			continue
		}
		if seen == roleOrdinal {
			return item
		}
		seen++
	}
	return nil
}

func ensureJSONArray(object map[string]any, key string, convert func(any) any) []any {
	if values, ok := object[key].([]any); ok {
		return values
	}
	if converted := convert(object[key]); converted != nil {
		return []any{converted}
	}
	return []any{}
}

func openAIChatAttachmentPart(attachment relayAttachment) map[string]any {
	file := map[string]any{}
	if attachment.Filename != "" {
		file["filename"] = attachment.Filename
	}
	if attachment.Data != "" {
		file["file_data"] = attachmentDataURL(attachment)
	} else if attachment.URL != "" {
		file["file_url"] = attachment.URL
	} else if attachment.ID != "" {
		file["file_id"] = attachment.ID
	}
	return map[string]any{"type": "file", "file": file}
}

func openAIResponsesAttachmentPart(attachment relayAttachment) map[string]any {
	part := map[string]any{"type": "input_file"}
	if attachment.Filename != "" {
		part["filename"] = attachment.Filename
	}
	if attachment.Data != "" {
		part["file_data"] = attachmentDataURL(attachment)
	} else if attachment.URL != "" {
		part["file_url"] = attachment.URL
	} else if attachment.ID != "" {
		part["file_id"] = attachment.ID
	}
	return part
}

func anthropicAttachmentPart(attachment relayAttachment) (map[string]any, bool) {
	if attachment.Data != "" {
		mediaType, data := attachmentBase64(attachment)
		if data == "" {
			return nil, false
		}
		return map[string]any{
			"type":   "document",
			"source": map[string]any{"type": "base64", "media_type": mediaType, "data": data},
		}, true
	}
	if attachment.URL != "" {
		return map[string]any{
			"type":   "document",
			"source": map[string]any{"type": "url", "url": attachment.URL},
		}, true
	}
	return nil, false
}

func geminiAttachmentPart(attachment relayAttachment) (map[string]any, bool) {
	if attachment.Data != "" {
		mediaType, data := attachmentBase64(attachment)
		if data == "" {
			return nil, false
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": data}}, true
	}
	if attachment.URL != "" {
		return map[string]any{"fileData": map[string]any{"mimeType": attachmentMimeType(attachment), "fileUri": attachment.URL}}, true
	}
	return nil, false
}

func relayAttachmentAlreadyPresent(parts []any, wanted relayAttachment) bool {
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		candidate := attachmentFromPart(part)
		if candidate == nil {
			continue
		}
		if wanted.ID != "" && candidate.ID == wanted.ID {
			return true
		}
		if wanted.URL != "" && candidate.URL == wanted.URL {
			return true
		}
		if wanted.Data != "" && attachmentDataURL(*candidate) == attachmentDataURL(wanted) {
			return true
		}
	}
	return false
}

func attachmentBase64(attachment relayAttachment) (string, string) {
	data := strings.TrimSpace(attachment.Data)
	if strings.HasPrefix(strings.ToLower(data), "data:") {
		meta, payload, ok := strings.Cut(data[5:], ",")
		if !ok {
			return attachmentMimeType(attachment), ""
		}
		parts := strings.Split(meta, ";")
		mediaType := parts[0]
		for _, part := range parts[1:] {
			if strings.EqualFold(strings.TrimSpace(part), "base64") {
				return firstNonEmptyString(mediaType, attachmentMimeType(attachment)), payload
			}
		}
		return firstNonEmptyString(mediaType, attachmentMimeType(attachment)), ""
	}
	return attachmentMimeType(attachment), data
}

func attachmentDataURL(attachment relayAttachment) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.Data)), "data:") {
		return attachment.Data
	}
	if attachment.Data == "" {
		return ""
	}
	return "data:" + attachmentMimeType(attachment) + ";base64," + attachment.Data
}

func attachmentMimeType(attachment relayAttachment) string {
	if value := strings.TrimSpace(attachment.MIMEType); value != "" {
		if mediaType, _, err := mime.ParseMediaType(value); err == nil {
			return mediaType
		}
		return value
	}
	if data := strings.TrimSpace(attachment.Data); strings.HasPrefix(strings.ToLower(data), "data:") {
		if meta, _, ok := strings.Cut(data[5:], ","); ok {
			if mediaType, _, ok := strings.Cut(meta, ";"); ok && mediaType != "" {
				return mediaType
			}
		}
	}
	if attachment.Filename != "" {
		if value := mime.TypeByExtension(filepath.Ext(attachment.Filename)); value != "" {
			return value
		}
	}
	return "application/octet-stream"
}

func normalizeAttachmentRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "user"
	}
	return role
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}
