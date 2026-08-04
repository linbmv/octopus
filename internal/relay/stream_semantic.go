package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

var errPreSemanticStreamBufferExceeded = errors.New("upstream stream exceeded pre-content buffer limit")

const maxPreSemanticStreamEvents = 256

// preSemanticStreamBuffer keeps protocol setup events private until actual
// model output commits the stream to this upstream. A failed candidate can then
// be discarded without exposing a partial response or replaying control events.
type preSemanticStreamBuffer struct {
	events []*httpclient.StreamEvent
	bytes  int
}

func (b *preSemanticStreamBuffer) add(event *httpclient.StreamEvent) error {
	if event == nil {
		return nil
	}
	size := len(event.Type) + len(event.LastEventID) + len(event.Data) + 16
	if len(b.events) >= maxPreSemanticStreamEvents || b.bytes+size > conf.MaxRelayLogContentBytes {
		return errPreSemanticStreamBufferExceeded
	}
	copyEvent := *event
	copyEvent.Data = append([]byte(nil), event.Data...)
	b.events = append(b.events, &copyEvent)
	b.bytes += size
	return nil
}

func (b *preSemanticStreamBuffer) flush(write func(*httpclient.StreamEvent)) {
	if b == nil || write == nil {
		return
	}
	for _, event := range b.events {
		write(event)
	}
	b.events = nil
	b.bytes = 0
}

// streamEventHasSemanticOutput separates protocol lifecycle events from model
// output. Unknown non-JSON payloads remain compatible by committing the stream,
// while known OpenAI, Responses, Anthropic and Gemini envelopes are inspected
// for text, reasoning, media or tool-call deltas.
func streamEventHasSemanticOutput(event *httpclient.StreamEvent) bool {
	if event == nil || len(event.Data) == 0 || bytes.Equal(bytes.TrimSpace(event.Data), llm.DoneStreamEvent.Data) {
		return false
	}
	if event.IsBinaryAudioChunk() {
		return len(event.Data) > 0 || event.Size > 0
	}

	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if isPreSemanticControlEvent(eventType) {
		return false
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return true
	}
	if streamPayloadHasSemanticOutput(payload) {
		return true
	}
	if isKnownSemanticEventType(eventType) {
		// A recognized output event with an opaque provider-specific payload has
		// crossed the replay boundary even if the generic decoder cannot find its
		// exact field shape.
		return !hasKnownStreamEnvelope(payload)
	}
	if hasKnownStreamEnvelope(payload) {
		return false
	}
	// Preserve compatibility for custom SSE protocols that emit arbitrary JSON
	// as their content event without using one of the standard envelope fields.
	return true
}

func isPreSemanticControlEvent(eventType string) bool {
	switch eventType {
	case "response.created", "response.queued", "response.in_progress",
		"response.completed", "response.incomplete", "response.failed",
		"message_start", "message_stop", "message_delta", "ping",
		"content_block_stop", "response.output_item.done", "response.content_part.done",
		"speech.audio.done", "transcript.text.done", httpclient.BinaryStreamDoneEventType:
		return true
	default:
		return false
	}
}

func isKnownSemanticEventType(eventType string) bool {
	switch eventType {
	case "response.output_text.delta", "response.output_text.done",
		"response.refusal.delta", "response.refusal.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.reasoning_text.delta", "response.reasoning_summary_text.delta",
		"response.image_generation_call.partial_image", "response.audio.delta",
		"speech.audio.delta", "transcript.text.delta", "content_block_start", "content_block_delta",
		"response.output_item.added", "response.content_part.added":
		return true
	default:
		return false
	}
}

func hasKnownStreamEnvelope(payload map[string]any) bool {
	for _, key := range []string{
		"type", "response", "message", "usage", "choices", "candidates", "delta",
		"item", "part", "content_block", "sequence_number", "output_index", "content_index",
	} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func streamPayloadHasSemanticOutput(payload map[string]any) bool {
	if semanticDeltaMap(payload["delta"]) || meaningfulStreamValue(payload["text"]) ||
		meaningfulStreamValue(payload["refusal"]) || meaningfulStreamValue(payload["arguments"]) ||
		meaningfulStreamValue(payload["input"]) || meaningfulStreamValue(payload["partial_image_b64"]) ||
		meaningfulStreamValue(payload["annotation"]) || meaningfulStreamValue(payload["citation"]) ||
		semanticMessageMap(payload["message"]) {
		return true
	}

	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			if meaningfulStreamValue(choice["text"]) || semanticDeltaMap(choice["delta"]) || semanticMessageMap(choice["message"]) {
				return true
			}
		}
	}
	if candidates, ok := payload["candidates"].([]any); ok {
		for _, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]any)
			if semanticMessageMap(candidate["content"]) {
				return true
			}
		}
	}
	for _, key := range []string{"delta", "content_block", "item", "part"} {
		if semanticOutputObject(payload[key]) {
			return true
		}
	}
	return false
}

func semanticDeltaMap(value any) bool {
	delta, ok := value.(map[string]any)
	if !ok {
		return meaningfulStreamValue(value)
	}
	for _, key := range []string{
		"content", "text", "refusal", "reasoning_content", "thinking", "signature",
		"partial_json", "tool_calls", "function_call", "functionCall", "citation", "audio",
	} {
		if meaningfulStreamValue(delta[key]) {
			return true
		}
	}
	return false
}

func semanticMessageMap(value any) bool {
	message, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"tool_calls", "function_call", "functionCall"} {
		if meaningfulStreamValue(message[key]) {
			return true
		}
	}
	for _, key := range []string{"content", "text", "parts"} {
		if semanticContentValue(message[key]) {
			return true
		}
	}
	return false
}

func semanticOutputObject(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	typeName, _ := object["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if strings.Contains(typeName, "tool") || strings.Contains(typeName, "function_call") ||
		typeName == "redacted_thinking" {
		return true
	}
	for _, key := range []string{"tool_calls", "function_call", "functionCall", "inline_data", "inlineData"} {
		if meaningfulStreamValue(object[key]) {
			return true
		}
	}
	for _, key := range []string{
		"content", "text", "refusal", "thinking", "data", "delta", "partial_json",
		"arguments", "input", "parts",
	} {
		if semanticContentValue(object[key]) {
			return true
		}
	}
	return false
}

func semanticContentValue(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			switch itemValue := item.(type) {
			case map[string]any:
				if semanticOutputObject(itemValue) {
					return true
				}
			default:
				if meaningfulStreamValue(itemValue) {
					return true
				}
			}
		}
		return false
	case map[string]any:
		return semanticOutputObject(typed)
	default:
		return meaningfulStreamValue(value)
	}
}

func meaningfulStreamValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case nil:
		return false
	default:
		return true
	}
}
