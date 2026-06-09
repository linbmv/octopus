package relay

import (
	"context"
	"strings"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

const (
	testBaseURL = "https://example.com/v1"
	testAPIKey  = "test-key"
)

func TestNewInboundKeepsCurrentPublicFormats(t *testing.T) {
	tests := []struct {
		name   string
		format llm.APIFormat
	}{
		{"openai_chat", llm.APIFormatOpenAIChatCompletion},
		{"openai_responses", llm.APIFormatOpenAIResponse},
		{"openai_responses_compact", llm.APIFormatOpenAIResponseCompact},
		{"openai_embeddings", llm.APIFormatOpenAIEmbedding},
		{"openai_image_generation", llm.APIFormatOpenAIImageGeneration},
		{"openai_image_edit", llm.APIFormatOpenAIImageEdit},
		{"openai_image_variation", llm.APIFormatOpenAIImageVariation},
		{"anthropic_messages", llm.APIFormatAnthropicMessage},
		{"gemini_contents", llm.APIFormatGeminiContents},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newInbound(tt.format); got == nil {
				t.Fatalf("newInbound(%s) returned nil", tt.format)
			}
		})
	}
}

func TestGeminiInboundParsesGenerateContentRequest(t *testing.T) {
	inbound := newInbound(llm.APIFormatGeminiContents)
	if inbound == nil {
		t.Fatal("newInbound returned nil Gemini inbound")
	}

	request, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1beta/models/gemini-1.5-flash:generateContent",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
	})
	if err != nil {
		t.Fatalf("TransformRequest returned error: %v", err)
	}
	if request.Model != "gemini-1.5-flash" {
		t.Fatalf("model = %q, want gemini-1.5-flash", request.Model)
	}
	if request.RequestType != llm.RequestTypeChat {
		t.Fatalf("request type = %q, want %q", request.RequestType, llm.RequestTypeChat)
	}
	if request.Stream == nil || *request.Stream {
		t.Fatalf("stream = %v, want false", request.Stream)
	}
}

func TestNewInboundDoesNotExposeNewUpstreamFormats(t *testing.T) {
	tests := []struct {
		name   string
		format llm.APIFormat
	}{
		{"openai_completions", llm.APIFormatOpenAICompletion},
		{"openai_video", llm.APIFormatOpenAIVideo},
		{"gemini_embeddings", llm.APIFormatGeminiEmbedding},
		{"jina_embeddings", llm.APIFormatJinaEmbedding},
		{"ollama_chat", llm.APIFormatOllamaChat},
		{"jina_rerank", llm.APIFormatJinaRerank},
		{"seedance_video", llm.APIFormatSeedanceVideo},
		{"unknown", llm.APIFormat("unknown/provider")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newInbound(tt.format); got != nil {
				t.Fatalf("newInbound(%s) returned %T, want nil", tt.format, got)
			}
		})
	}
}

func TestNewOutboundKeepsCurrentChatCompatibility(t *testing.T) {
	assertOutboundCompatible(t, []outboundCase{
		{"openai_chat", llm.APIFormatOpenAIChatCompletion, llm.RequestTypeChat},
		{"openai_responses", llm.APIFormatOpenAIResponse, llm.RequestTypeChat},
		{"anthropic_messages", llm.APIFormatAnthropicMessage, llm.RequestTypeChat},
		{"gemini_contents", llm.APIFormatGeminiContents, llm.RequestTypeChat},
		{"doubao", dbmodel.ChannelTypeDoubao, llm.RequestTypeChat},
	})
}

func TestNewOutboundKeepsCurrentEmbeddingCompatibility(t *testing.T) {
	assertOutboundCompatible(t, []outboundCase{
		{"openai_chat", llm.APIFormatOpenAIChatCompletion, llm.RequestTypeEmbedding},
		{"openai_responses", llm.APIFormatOpenAIResponse, llm.RequestTypeEmbedding},
		{"openai_embeddings", llm.APIFormatOpenAIEmbedding, llm.RequestTypeEmbedding},
		{"gemini_contents", llm.APIFormatGeminiContents, llm.RequestTypeEmbedding},
		{"doubao", dbmodel.ChannelTypeDoubao, llm.RequestTypeEmbedding},
	})
}

func TestNewOutboundKeepsCurrentImageCompatibility(t *testing.T) {
	assertOutboundCompatible(t, []outboundCase{
		{"openai_chat", llm.APIFormatOpenAIChatCompletion, llm.RequestTypeImage},
		{"openai_responses", llm.APIFormatOpenAIResponse, llm.RequestTypeImage},
		{"openai_image_generation", llm.APIFormatOpenAIImageGeneration, llm.RequestTypeImage},
		{"openai_image_edit", llm.APIFormatOpenAIImageEdit, llm.RequestTypeImage},
		{"openai_image_variation", llm.APIFormatOpenAIImageVariation, llm.RequestTypeImage},
		{"gemini_contents", llm.APIFormatGeminiContents, llm.RequestTypeImage},
		{"doubao", dbmodel.ChannelTypeDoubao, llm.RequestTypeImage},
	})
}

func TestNewOutboundKeepsCurrentCompactCompatibility(t *testing.T) {
	assertOutboundCompatible(t, []outboundCase{
		{"openai_chat", llm.APIFormatOpenAIChatCompletion, llm.RequestTypeCompact},
		{"openai_responses", llm.APIFormatOpenAIResponse, llm.RequestTypeCompact},
		{"openai_responses_compact", llm.APIFormatOpenAIResponseCompact, llm.RequestTypeCompact},
	})
}

func TestNewOutboundCompactOpenAIChatDoesNotMutateRequest(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact}
	got, err := newOutbound(llm.APIFormatOpenAIChatCompletion, req, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound returned error: %v", err)
	}
	if got == nil {
		t.Fatal("newOutbound returned nil outbound")
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("RequestType 被原地修改为 %q，期望保持 %q", req.RequestType, llm.RequestTypeCompact)
	}
}

func TestNewOutboundCompactChatFallbackDoesNotPolluteResponseRetry(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact}
	chatOutbound, err := newOutbound(llm.APIFormatOpenAIChatCompletion, req, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("OpenAI Chat Compact 降级应返回 outbound: %v", err)
	}
	if chatOutbound == nil {
		t.Fatal("OpenAI Chat Compact 降级返回 nil outbound")
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Chat 降级污染了共享请求类型: %q", req.RequestType)
	}

	responseOutbound, err := newOutbound(llm.APIFormatOpenAIResponse, req, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("后续 OpenAI Response 渠道应仍能按 Compact 请求处理: %v", err)
	}
	if responseOutbound == nil {
		t.Fatal("OpenAI Response Compact 返回 nil outbound")
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("Response 重试后请求类型应仍保持 Compact，got %q", req.RequestType)
	}
}

func TestRequestForOutboundPipelineDowngradesCompactChatOnCopy(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact, Model: "gpt-5.5"}
	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got == req {
		t.Fatal("requestForOutboundPipeline 应返回当前 attempt 的请求副本")
	}
	if got.RequestType != llm.RequestTypeChat {
		t.Fatalf("OpenAI Chat Compact 副本 RequestType = %q，期望 %q", got.RequestType, llm.RequestTypeChat)
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始请求被污染为 %q，期望保持 Compact", req.RequestType)
	}
}

func TestRequestForOutboundPipelineKeepsCompactForResponseChannel(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact, Model: "gpt-5.5"}
	got := requestForOutboundPipeline(llm.APIFormatOpenAIResponse, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Response 请求副本 RequestType = %q，期望保持 Compact", got.RequestType)
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始请求被污染为 %q，期望保持 Compact", req.RequestType)
	}
}

func TestNeedsChatToCompactResponse(t *testing.T) {
	if !needsChatToCompactResponse(llm.APIFormatOpenAIChatCompletion, &llm.Request{RequestType: llm.RequestTypeCompact}) {
		t.Fatal("OpenAI Chat Compact 请求应启用 Chat→Compact 响应转换")
	}
	if needsChatToCompactResponse(llm.APIFormatOpenAIResponse, &llm.Request{RequestType: llm.RequestTypeCompact}) {
		t.Fatal("OpenAI Response Compact 原生路径不应启用 Chat→Compact 响应转换")
	}
	if needsChatToCompactResponse(llm.APIFormatOpenAIChatCompletion, &llm.Request{RequestType: llm.RequestTypeChat}) {
		t.Fatal("普通 Chat 请求不应启用 Chat→Compact 响应转换")
	}
	if needsChatToCompactResponse(llm.APIFormatOpenAIChatCompletion, nil) {
		t.Fatal("nil 请求不应启用 Chat→Compact 响应转换")
	}
}

// strPtr 返回字符串内容指针，便于构造 MessageContent。
func strPtr(value string) *string {
	return &value
}

func compactInputMessage(role, text string) llm.Message {
	content := text
	return llm.Message{Role: role, Content: llm.MessageContent{Content: &content}}
}

func TestCompactChatFallbackRequestFillsMessages(t *testing.T) {
	req := &llm.Request{
		Model:       "gpt-5.5",
		RequestType: llm.RequestTypeCompact,
		Compact: &llm.CompactRequest{
			Instructions: "you are a summarizer",
			Input: []llm.Message{
				compactInputMessage("user", "hello"),
				compactInputMessage("assistant", "hi"),
			},
		},
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got.RequestType != llm.RequestTypeChat {
		t.Fatalf("RequestType = %q, 期望降级为 Chat", got.RequestType)
	}
	// Instructions(1) + Input(2) = 3 条消息
	if len(got.Messages) != 3 {
		t.Fatalf("Messages 长度 = %d, 期望 3（system + 2 条 input）", len(got.Messages))
	}
	if got.Messages[0].Role != "system" {
		t.Fatalf("首条消息 role = %q, 期望 system", got.Messages[0].Role)
	}
	if got.Messages[0].Content.Content == nil || *got.Messages[0].Content.Content != "you are a summarizer" {
		t.Fatalf("system 消息内容未正确搬运 Instructions")
	}
	if got.Messages[1].Role != "user" || got.Messages[2].Role != "assistant" {
		t.Fatalf("Input 消息顺序/角色未保持: %q, %q", got.Messages[1].Role, got.Messages[2].Role)
	}

	// 原始请求不能被污染。
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始 RequestType 被污染为 %q", req.RequestType)
	}
	if len(req.Messages) != 0 {
		t.Fatalf("原始 Messages 被污染，长度 = %d，期望 0", len(req.Messages))
	}
	if len(req.Compact.Input) != 2 {
		t.Fatalf("原始 Compact.Input 被污染，长度 = %d，期望 2", len(req.Compact.Input))
	}
}

func TestCompactResponsesFallbackRequestUsesStandardResponsesShape(t *testing.T) {
	req := &llm.Request{
		Model:       "gpt-5.5",
		RequestType: llm.RequestTypeCompact,
		Compact: &llm.CompactRequest{
			Instructions: "summarize",
			Input:        []llm.Message{compactInputMessage("user", "hello")},
		},
	}

	got := compactResponsesFallbackRequest(req)
	if got == nil {
		t.Fatal("compactResponsesFallbackRequest returned nil")
	}
	if got.RequestType != llm.RequestTypeChat {
		t.Fatalf("RequestType = %q, 期望降级为 Chat", got.RequestType)
	}
	if got.APIFormat != llm.APIFormatOpenAIResponse {
		t.Fatalf("APIFormat = %q, 期望 OpenAI Responses", got.APIFormat)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("Messages 长度 = %d, 期望 2（system + input）", len(got.Messages))
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content.Content == nil || *got.Messages[0].Content.Content != "summarize" {
		t.Fatalf("Instructions 未正确搬运到 system 消息: %#v", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content.Content == nil || *got.Messages[1].Content.Content != "hello" {
		t.Fatalf("Compact.Input 未正确搬运到 Messages: %#v", got.Messages[1])
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始 RequestType 被污染为 %q", req.RequestType)
	}
	if len(req.Messages) != 0 {
		t.Fatalf("原始 Messages 被污染，长度 = %d，期望 0", len(req.Messages))
	}
}

func TestCompactChatFallbackRequestNoInstructions(t *testing.T) {
	req := &llm.Request{
		Model:       "gpt-5.5",
		RequestType: llm.RequestTypeCompact,
		Compact: &llm.CompactRequest{
			Input: []llm.Message{compactInputMessage("user", "hello")},
		},
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if len(got.Messages) != 1 {
		t.Fatalf("Messages 长度 = %d, 期望 1（无 Instructions）", len(got.Messages))
	}
	if got.Messages[0].Role != "user" {
		t.Fatalf("首条消息 role = %q, 期望 user（无 system 头）", got.Messages[0].Role)
	}
}

func TestCompactChatFallbackRequestNilCompact(t *testing.T) {
	req := &llm.Request{
		Model:       "gpt-5.5",
		RequestType: llm.RequestTypeCompact,
		Compact:     nil,
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got.RequestType != llm.RequestTypeChat {
		t.Fatalf("RequestType = %q, 期望 Chat", got.RequestType)
	}
	// Compact 为 nil 时不应 panic，Messages 保持原样（空）。
	if len(got.Messages) != 0 {
		t.Fatalf("Compact 为 nil 时 Messages 应为空，got %d", len(got.Messages))
	}
}

func TestCompactChatFallbackRequestTranscriptsToolHistory(t *testing.T) {
	forcedCustom := &llm.ToolChoice{
		NamedToolChoice: &llm.NamedToolChoice{
			Type:     llm.ToolTypeResponsesCustomTool,
			Function: llm.ToolFunction{Name: "apply_patch"},
		},
	}
	parallel := true
	req := &llm.Request{
		Model:             "gpt-5.5",
		RequestType:       llm.RequestTypeCompact,
		ParallelToolCalls: &parallel,
		ToolChoice:        forcedCustom,
		Tools: []llm.Tool{
			{Type: llm.ToolTypeResponsesCustomTool, ResponseCustomTool: &llm.ResponseCustomTool{Name: "apply_patch"}},
			{Type: llm.ToolTypeFunction, Function: llm.Function{Name: ""}},
			{Type: llm.ToolTypeFunction, Function: llm.Function{Name: "read_file"}},
		},
		Compact: &llm.CompactRequest{
			Input: []llm.Message{
				{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{ID: "custom", Type: llm.ToolTypeResponsesCustomTool, ResponseCustomToolCall: &llm.ResponseCustomToolCall{Name: "apply_patch", Input: "*** Begin Patch\n"}},
						{ID: "empty", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: ""}},
						{ID: "fn", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}},
					},
				},
			},
		},
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got.APIFormat != llm.APIFormatOpenAIChatCompletion {
		t.Fatalf("APIFormat = %q, 期望 OpenAI Chat", got.APIFormat)
	}
	if got.ParallelToolCalls != nil {
		t.Fatalf("Compact Chat fallback 不应发送 ParallelToolCalls，got %#v", got.ParallelToolCalls)
	}
	if len(got.Tools) != 0 {
		t.Fatalf("Compact Chat fallback 不应发送工具定义: %#v", got.Tools)
	}
	if got.ToolChoice != nil {
		t.Fatalf("Compact Chat fallback 不应发送 ToolChoice: %#v", got.ToolChoice)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("Messages 长度 = %d, 期望 1", len(got.Messages))
	}
	if len(got.Messages[0].ToolCalls) != 0 {
		t.Fatalf("真实 ToolCalls 应转成文本而不是继续发送: %#v", got.Messages[0].ToolCalls)
	}
	if got.Messages[0].Content.Content == nil {
		t.Fatal("工具调用 transcript 应写入普通文本 content")
	}
	content := *got.Messages[0].Content.Content
	if !strings.Contains(content, "[tool call name=apply_patch id=custom]") || !strings.Contains(content, "*** Begin Patch") {
		t.Fatalf("Responses custom tool call 未进入 transcript: %q", content)
	}
	if !strings.Contains(content, "[tool call name=read_file id=fn]") || !strings.Contains(content, "{}") {
		t.Fatalf("function tool call 未进入 transcript: %q", content)
	}

	if len(req.Tools) != 3 {
		t.Fatalf("原始 Tools 被污染，长度 = %d", len(req.Tools))
	}
	if len(req.Compact.Input[0].ToolCalls) != 3 {
		t.Fatalf("原始 Compact.Input ToolCalls 被污染，长度 = %d", len(req.Compact.Input[0].ToolCalls))
	}
}

func TestCompactChatFallbackRequestConvertsToolResultsToUserText(t *testing.T) {
	callID := "call_123"
	toolName := "read_file"
	req := &llm.Request{
		Model:       "gpt-5.5",
		RequestType: llm.RequestTypeCompact,
		Compact: &llm.CompactRequest{
			Input: []llm.Message{
				{
					Role:         "tool",
					ToolCallID:   &callID,
					ToolCallName: &toolName,
					Content:      llm.MessageContent{Content: strPtr(`{"ok":true}`)},
				},
			},
		},
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if len(got.Messages) != 1 {
		t.Fatalf("Messages 长度 = %d, 期望 1", len(got.Messages))
	}
	msg := got.Messages[0]
	if msg.Role != "user" {
		t.Fatalf("tool 结果应转为普通 user 文本，got role=%q", msg.Role)
	}
	if msg.ToolCallID != nil || msg.ToolCallName != nil || len(msg.ToolCalls) != 0 {
		t.Fatalf("tool 协议字段应被清空: %#v", msg)
	}
	if msg.Content.Content == nil || !strings.Contains(*msg.Content.Content, "[tool result name=read_file id=call_123]") {
		t.Fatalf("tool 结果 transcript 不正确: %#v", msg.Content.Content)
	}
}

func TestCompactChatFallbackRequestClearsParallelToolCallsWhenNoChatTools(t *testing.T) {
	parallel := true
	req := &llm.Request{
		Model:             "gpt-5.5",
		RequestType:       llm.RequestTypeCompact,
		ParallelToolCalls: &parallel,
		ToolChoice:        &llm.ToolChoice{ToolChoice: strPtr("required")},
		Tools: []llm.Tool{
			{Type: llm.ToolTypeResponsesCustomTool, ResponseCustomTool: &llm.ResponseCustomTool{Name: "apply_patch"}},
		},
		Compact: &llm.CompactRequest{Input: []llm.Message{compactInputMessage("user", "hello")}},
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if len(got.Tools) != 0 {
		t.Fatalf("不可发送工具应全部清理，got %#v", got.Tools)
	}
	if got.ToolChoice != nil {
		t.Fatalf("没有可发送工具时 ToolChoice 应被清理，got %#v", got.ToolChoice)
	}
	if got.ParallelToolCalls != nil {
		t.Fatalf("没有可发送工具时 ParallelToolCalls 应被清理，got %#v", got.ParallelToolCalls)
	}
}

func TestRequestForOutboundPipelineDeepCopiesStreamPointer(t *testing.T) {
	streamTrue := true
	req := &llm.Request{
		RequestType: llm.RequestTypeChat,
		Model:       "gpt-4",
		Stream:      &streamTrue,
	}

	copy1 := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	copy2 := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)

	if copy1 == nil || copy2 == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}

	// 验证副本的 Stream 指针不同于原始请求
	if copy1.Stream == req.Stream {
		t.Fatal("copy1.Stream 应该是深拷贝，不应与原始请求共享同一指针")
	}
	if copy2.Stream == req.Stream {
		t.Fatal("copy2.Stream 应该是深拷贝，不应与原始请求共享同一指针")
	}

	// 验证多个副本之间的 Stream 指针也不同
	if copy1.Stream == copy2.Stream {
		t.Fatal("copy1 和 copy2 的 Stream 应该是独立拷贝，不应共享同一指针")
	}

	// 验证修改副本的 Stream 不影响原始请求
	*copy1.Stream = false
	if *req.Stream != true {
		t.Fatal("修改 copy1.Stream 污染了原始请求")
	}
	if *copy2.Stream != true {
		t.Fatal("修改 copy1.Stream 污染了 copy2")
	}
}

func TestRequestForOutboundPipelineHandlesNilStream(t *testing.T) {
	req := &llm.Request{
		RequestType: llm.RequestTypeChat,
		Model:       "gpt-4",
		Stream:      nil,
	}

	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
	}
	if got.Stream != nil {
		t.Fatalf("Stream 应保持 nil，got %v", got.Stream)
	}
}

func TestNewOutboundDefaultsNilRequestToChat(t *testing.T) {
	got, err := newOutbound(llm.APIFormatOpenAIChatCompletion, nil, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound returned error: %v", err)
	}
	if got == nil {
		t.Fatal("newOutbound returned nil outbound")
	}
}

func TestNewOutboundRejectsUnsupportedRequestTypesExplicitly(t *testing.T) {
	tests := []llm.RequestType{
		llm.RequestTypeRerank,
		llm.RequestTypeVideo,
		llm.RequestTypeCompletion,
		llm.RequestType("unknown"),
	}

	for _, requestType := range tests {
		t.Run(requestType.String(), func(t *testing.T) {
			got, err := newOutbound(llm.APIFormatOpenAIChatCompletion, &llm.Request{RequestType: requestType}, testBaseURL, testAPIKey)
			if err == nil {
				t.Fatalf("newOutbound returned nil error and %T", got)
			}
			if got != nil {
				t.Fatalf("newOutbound returned %T, want nil", got)
			}
			if !strings.Contains(err.Error(), "request is not supported by relay") {
				t.Fatalf("error = %q, want unsupported relay error", err.Error())
			}
		})
	}
}

func TestNewOutboundRejectsNewUpstreamFormats(t *testing.T) {
	tests := []outboundCase{
		{"openai_completions", llm.APIFormatOpenAICompletion, llm.RequestTypeCompletion},
		{"openai_video", llm.APIFormatOpenAIVideo, llm.RequestTypeVideo},
		{"gemini_embeddings", llm.APIFormatGeminiEmbedding, llm.RequestTypeEmbedding},
		{"jina_embeddings", llm.APIFormatJinaEmbedding, llm.RequestTypeEmbedding},
		{"jina_rerank", llm.APIFormatJinaRerank, llm.RequestTypeRerank},
		{"ollama_chat", llm.APIFormatOllamaChat, llm.RequestTypeChat},
		{"seedance_video", llm.APIFormatSeedanceVideo, llm.RequestTypeVideo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newOutbound(tt.channelType, &llm.Request{RequestType: tt.requestType}, testBaseURL, testAPIKey)
			if err == nil {
				t.Fatalf("newOutbound returned nil error and %T", got)
			}
			if got != nil {
				t.Fatalf("newOutbound returned %T, want nil", got)
			}
		})
	}
}

type outboundCase struct {
	name        string
	channelType llm.APIFormat
	requestType llm.RequestType
}

func assertOutboundCompatible(t *testing.T, tests []outboundCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newOutbound(tt.channelType, &llm.Request{RequestType: tt.requestType}, testBaseURL, testAPIKey)
			if err != nil {
				t.Fatalf("newOutbound returned error: %v", err)
			}
			if got == nil {
				t.Fatal("newOutbound returned nil outbound")
			}
		})
	}
}
