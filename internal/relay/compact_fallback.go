package relay

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

// cloneRequestForAttempt 为单次通道尝试构造请求副本：浅拷贝值类型字段，
// 并深拷贝会被 transformer/middleware 原地修改的引用类型字段，避免多 attempt 互相污染。
// RawRequest 由 parsedRequestInbound.TransformRequest 每次重新赋值，不在此处拷贝。
func cloneRequestForAttempt(request *llm.Request) *llm.Request {
	if request == nil {
		return nil
	}

	attemptRequest := *request

	if request.Stream != nil {
		streamCopy := *request.Stream
		attemptRequest.Stream = &streamCopy
	}
	if request.StreamOptions != nil {
		streamOptionsCopy := *request.StreamOptions
		attemptRequest.StreamOptions = &streamOptionsCopy
	}
	if request.LogitBias != nil {
		attemptRequest.LogitBias = maps.Clone(request.LogitBias)
	}
	if request.Metadata != nil {
		attemptRequest.Metadata = maps.Clone(request.Metadata)
	}
	if request.Modalities != nil {
		attemptRequest.Modalities = slices.Clone(request.Modalities)
	}

	return &attemptRequest
}

// requestForOutboundPipeline 返回当前通道尝试要交给 pipeline 的请求副本。
// OpenAI Chat 渠道承接 Compact 请求时，必须降级为 Chat 并把 Compact.Input 搬到 Messages，
// 否则 axonhub 的 openai outbound 会因 Messages 为空而报 "messages are required"。
func requestForOutboundPipeline(channelType llm.APIFormat, request *llm.Request) *llm.Request {
	if request == nil {
		return nil
	}

	if channelType == llm.APIFormatOpenAIChatCompletion && request.RequestType == llm.RequestTypeCompact {
		return compactChatFallbackRequest(request)
	}

	return cloneRequestForAttempt(request)
}

// compactOfficialRequest 构造官方 /v1/responses/compact 尝试使用的请求副本。
func compactOfficialRequest(request *llm.Request) *llm.Request {
	attemptRequest := cloneRequestForAttempt(request)
	attemptRequest.RequestType = llm.RequestTypeCompact
	attemptRequest.APIFormat = llm.APIFormatOpenAIResponseCompact
	return attemptRequest
}

// compactResponsesFallbackRequest 把 Compact 请求降级成可由普通 OpenAI Responses 端点处理的副本。
func compactResponsesFallbackRequest(request *llm.Request) *llm.Request {
	attemptRequest := compactConversationFallbackRequest(request)
	attemptRequest.APIFormat = llm.APIFormatOpenAIResponse
	arrayInputs := true
	store := false
	attemptRequest.TransformOptions.ArrayInputs = &arrayInputs
	attemptRequest.Store = &store
	attemptRequest.MaxCompletionTokens = nil
	attemptRequest.MaxTokens = nil
	attemptRequest.Metadata = nil
	if request != nil && request.Compact != nil && strings.TrimSpace(request.Compact.PromptCacheKey) != "" {
		promptCacheKey := request.Compact.PromptCacheKey
		attemptRequest.PromptCacheKey = &promptCacheKey
	}
	return attemptRequest
}

// compactChatFallbackRequest 把 Compact 请求降级成可由 OpenAI Chat 端点处理的副本。
func compactChatFallbackRequest(request *llm.Request) *llm.Request {
	attemptRequest := compactConversationFallbackRequest(request)
	attemptRequest.APIFormat = llm.APIFormatOpenAIChatCompletion
	return attemptRequest
}

// compactConversationFallbackRequest 把 Compact 请求降级成普通对话请求副本：
//   - RequestType 改为 Chat（绕过 openai outbound 对 Compact 的拒绝）；
//   - 把 Compact.Input 搬到 Messages（axonhub Compact inbound 把消息放在 Compact.Input，Messages 恒为空）；
//   - Compact.Instructions 非空时，作为 system 消息插入到最前，保留系统指令。
//   - 工具调用历史转成普通 transcript 文本，不按 Chat Completions 工具协议发送。
//
// 全程基于副本和切片拷贝，不修改原始 request，保证多 attempt 重试安全。
func compactConversationFallbackRequest(request *llm.Request) *llm.Request {
	attemptRequest := cloneRequestForAttempt(request)
	attemptRequest.RequestType = llm.RequestTypeChat
	attemptRequest.Tools = nil
	attemptRequest.ToolChoice = nil
	attemptRequest.ParallelToolCalls = nil

	if request.Compact == nil {
		return attemptRequest
	}

	messages := make([]llm.Message, 0, len(request.Compact.Input)+1)
	if instructions := strings.TrimSpace(request.Compact.Instructions); instructions != "" {
		systemContent := request.Compact.Instructions
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: llm.MessageContent{Content: &systemContent},
		})
	}
	messages = append(messages, request.Compact.Input...)
	attemptRequest.Messages = transcriptMessagesForChatFallback(messages)

	return attemptRequest
}

func transcriptMessagesForChatFallback(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}

	transcript := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		text := transcriptTextForChatFallback(message)
		if strings.TrimSpace(text) == "" {
			continue
		}
		message.Role = transcriptRoleForChatFallback(message)
		message.Content = llm.MessageContent{Content: &text}
		message.MessageIndex = nil
		message.ToolCallID = nil
		message.ToolCallName = nil
		message.ToolCallIsError = nil
		message.ToolCalls = nil
		message.ReasoningContent = nil
		message.Reasoning = nil
		message.CacheControl = nil
		transcript = append(transcript, message)
	}
	return transcript
}

func transcriptRoleForChatFallback(message llm.Message) string {
	switch message.Role {
	case "assistant", "system", "user":
		return message.Role
	case "developer":
		return "system"
	default:
		return "user"
	}
}

func transcriptTextForChatFallback(message llm.Message) string {
	content := messageContentTextForChatFallback(message.Content)
	if message.Role == "tool" || message.ToolCallID != nil || message.ToolCallName != nil {
		return toolResultTextForChatFallback(message, content)
	}

	parts := make([]string, 0, len(message.ToolCalls)+2)
	if strings.TrimSpace(content) != "" {
		parts = append(parts, content)
	}
	if message.ReasoningContent != nil && strings.TrimSpace(*message.ReasoningContent) != "" {
		parts = append(parts, "[reasoning]\n"+*message.ReasoningContent)
	} else if message.Reasoning != nil && strings.TrimSpace(*message.Reasoning) != "" {
		parts = append(parts, "[reasoning]\n"+*message.Reasoning)
	}
	for _, toolCall := range message.ToolCalls {
		if text := toolCallTextForChatFallback(toolCall); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func toolResultTextForChatFallback(message llm.Message, content string) string {
	details := make([]string, 0, 3)
	if message.ToolCallName != nil && strings.TrimSpace(*message.ToolCallName) != "" {
		details = append(details, "name="+*message.ToolCallName)
	}
	if message.ToolCallID != nil && strings.TrimSpace(*message.ToolCallID) != "" {
		details = append(details, "id="+*message.ToolCallID)
	}
	if message.ToolCallIsError != nil && *message.ToolCallIsError {
		details = append(details, "error=true")
	}

	header := "[tool result]"
	if len(details) > 0 {
		header = "[tool result " + strings.Join(details, " ") + "]"
	}
	if strings.TrimSpace(content) == "" {
		return header
	}
	return header + "\n" + content
}

func toolCallTextForChatFallback(toolCall llm.ToolCall) string {
	name := strings.TrimSpace(toolCall.Function.Name)
	input := toolCall.Function.Arguments
	if toolCall.ResponseCustomToolCall != nil {
		if strings.TrimSpace(toolCall.ResponseCustomToolCall.Name) != "" {
			name = strings.TrimSpace(toolCall.ResponseCustomToolCall.Name)
		}
		input = toolCall.ResponseCustomToolCall.Input
	}
	if name == "" && strings.TrimSpace(toolCall.ID) == "" && strings.TrimSpace(input) == "" {
		return ""
	}

	details := make([]string, 0, 2)
	if name != "" {
		details = append(details, "name="+name)
	}
	if strings.TrimSpace(toolCall.ID) != "" {
		details = append(details, "id="+toolCall.ID)
	}
	header := "[tool call]"
	if len(details) > 0 {
		header = "[tool call " + strings.Join(details, " ") + "]"
	}
	if strings.TrimSpace(input) == "" {
		return header
	}
	return header + "\n" + input
}

func messageContentTextForChatFallback(content llm.MessageContent) string {
	if content.Content != nil {
		return *content.Content
	}
	if len(content.MultipleContent) == 0 {
		return ""
	}

	parts := make([]string, 0, len(content.MultipleContent))
	for _, part := range content.MultipleContent {
		switch part.Type {
		case "text":
			if part.Text != nil && strings.TrimSpace(*part.Text) != "" {
				parts = append(parts, *part.Text)
			}
		case "image_url":
			if part.ImageURL != nil && strings.TrimSpace(part.ImageURL.URL) != "" {
				parts = append(parts, "[image_url] "+part.ImageURL.URL)
			}
		case "video_url":
			if part.VideoURL != nil && strings.TrimSpace(part.VideoURL.URL) != "" {
				parts = append(parts, "[video_url] "+part.VideoURL.URL)
			}
		case "input_audio":
			if part.InputAudio != nil {
				parts = append(parts, "[input_audio] format="+part.InputAudio.Format)
			}
		case "compaction", "compaction_summary":
			if part.Compact != nil && strings.TrimSpace(part.Compact.EncryptedContent) != "" {
				parts = append(parts, "["+part.Type+"] "+part.Compact.EncryptedContent)
			}
		default:
			if strings.TrimSpace(part.Type) != "" {
				parts = append(parts, "["+part.Type+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

// chatToCompactMiddleware 将 Chat 形态的响应转换为 Compact 格式。
// 用于手动压缩路径：/v1/responses 或 /v1/chat/completions 返回 Choices，
// 但客户端期望收到 Compact API 响应，因此需要把 Choices 转换成 Compact 结构。
type chatToCompactMiddleware struct {
	pipeline.DummyMiddleware
}

func (c *chatToCompactMiddleware) Name() string {
	return "chatToCompact"
}

func (c *chatToCompactMiddleware) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	if response == nil {
		return response, nil
	}
	// Chat 端点返回的是 Choices 结构，需要转换为 Compact 结构
	if response.Compact == nil && len(response.Choices) > 0 {
		output := []llm.Message{}
		for _, choice := range response.Choices {
			if choice.Message != nil && messageHasCompactionContent(*choice.Message) {
				output = append(output, *choice.Message)
			}
		}
		if len(output) == 0 {
			return nil, fmt.Errorf("compact manual fallback returned no compaction output")
		}

		response.Compact = &llm.CompactResponse{
			ID:        response.ID,
			CreatedAt: response.Created,
			Object:    "response.compaction",
			Output:    output,
		}
	}
	if response.Compact == nil || !compactResponseHasCompactionOutput(response.Compact) {
		return nil, fmt.Errorf("compact manual fallback returned no compaction output")
	}
	response.RequestType = llm.RequestTypeCompact
	response.APIFormat = llm.APIFormatOpenAIResponseCompact

	return response, nil
}

func compactResponseHasCompactionOutput(compact *llm.CompactResponse) bool {
	if compact == nil {
		return false
	}
	for _, message := range compact.Output {
		if messageHasCompactionContent(message) {
			return true
		}
	}
	return false
}

func messageHasCompactionContent(message llm.Message) bool {
	for _, part := range message.Content.MultipleContent {
		if part.Compact != nil && (part.Type == "compaction" || part.Type == "compaction_summary") {
			return true
		}
	}
	return false
}

func (c *chatToCompactMiddleware) OnOutboundLlmStream(ctx context.Context, stream streams.Stream[*llm.Response]) (streams.Stream[*llm.Response], error) {
	// 流式响应：将 Chat delta 转换为 Compact delta
	return streams.Map(stream, func(event *llm.Response) *llm.Response {
		if event.Compact == nil && len(event.Choices) > 0 {
			firstChoice := event.Choices[0]
			delta := []llm.Message{}
			if firstChoice.Delta != nil {
				delta = append(delta, *firstChoice.Delta)
			}

			// 构造 Compact event
			event.Compact = &llm.CompactResponse{
				Object: "response.compaction.delta",
				Output: delta,
			}
		}
		return event
	}), nil
}
