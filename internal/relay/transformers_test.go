package relay

import (
	"context"
	"encoding/json"
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

func TestOpenAIImageGenerationRoundTripBuildsExpectedOutboundRequest(t *testing.T) {
	inbound := newInbound(llm.APIFormatOpenAIImageGeneration)
	if inbound == nil {
		t.Fatal("newInbound returned nil OpenAI image generation inbound")
	}

	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1/images/generations",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(`{"model":"dall-e-3","prompt":"a puppy","n":1,"size":"1024x1024","response_format":"url"}`),
	})
	if err != nil {
		t.Fatalf("TransformRequest returned error: %v", err)
	}
	if llmReq.Model != "dall-e-3" {
		t.Fatalf("model = %q, want dall-e-3", llmReq.Model)
	}
	if llmReq.RequestType != llm.RequestTypeImage {
		t.Fatalf("request type = %q, want %q", llmReq.RequestType, llm.RequestTypeImage)
	}
	if llmReq.APIFormat != llm.APIFormatOpenAIImageGeneration {
		t.Fatalf("api format = %q, want %q", llmReq.APIFormat, llm.APIFormatOpenAIImageGeneration)
	}
	if llmReq.Image == nil || llmReq.Image.Prompt != "a puppy" {
		t.Fatalf("image prompt not preserved: %#v", llmReq.Image)
	}

	outbound, err := newOutbound(llm.APIFormatOpenAIImageGeneration, llmReq, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound returned error: %v", err)
	}
	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	if err != nil {
		t.Fatalf("TransformRequest returned error: %v", err)
	}
	if httpReq.URL != testBaseURL+"/images/generations" {
		t.Fatalf("URL = %q, want %q", httpReq.URL, testBaseURL+"/images/generations")
	}
	if httpReq.APIFormat != string(llm.APIFormatOpenAIImageGeneration) {
		t.Fatalf("APIFormat = %q, want %q", httpReq.APIFormat, llm.APIFormatOpenAIImageGeneration)
	}
	if httpReq.RequestType != llm.RequestTypeImage.String() {
		t.Fatalf("RequestType = %q, want %q", httpReq.RequestType, llm.RequestTypeImage)
	}
	if got := httpReq.TransformerMetadata["model"]; got != "dall-e-3" {
		t.Fatalf("TransformerMetadata model = %#v, want dall-e-3", got)
	}

	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body["model"] != "dall-e-3" || body["prompt"] != "a puppy" {
		t.Fatalf("image model/prompt not preserved in outbound body: %#v", body)
	}
	if body["size"] != "1024x1024" || body["response_format"] != "url" {
		t.Fatalf("image options not preserved in outbound body: %#v", body)
	}
	if body["n"] != float64(1) {
		t.Fatalf("n = %#v, want 1", body["n"])
	}
}

func TestNewOutboundKeepsCurrentCompactCompatibility(t *testing.T) {
	assertOutboundCompatible(t, []outboundCase{
		{"openai_responses", llm.APIFormatOpenAIResponse, llm.RequestTypeCompact},
		{"openai_responses_compact", llm.APIFormatOpenAIResponseCompact, llm.RequestTypeCompact},
	})
}

func TestNewOutboundCompactOpenAIChatUnsupported(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact}
	got, err := newOutbound(llm.APIFormatOpenAIChatCompletion, req, testBaseURL, testAPIKey)
	if err == nil {
		t.Fatal("OpenAI Chat channel must not handle Compact requests")
	}
	if got != nil {
		t.Fatal("unsupported OpenAI Chat Compact should not return outbound")
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("RequestType 被原地修改为 %q，期望保持 %q", req.RequestType, llm.RequestTypeCompact)
	}
}

func TestNewOutboundCompactChatFailureDoesNotPolluteResponseRetry(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact}
	if chatOutbound, err := newOutbound(llm.APIFormatOpenAIChatCompletion, req, testBaseURL, testAPIKey); err == nil || chatOutbound != nil {
		t.Fatal("OpenAI Chat Compact should be rejected before Response retry")
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Chat rejection polluted shared request type: %q", req.RequestType)
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

func TestRequestForOutboundPipelineKeepsCompactForChatChannel(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact, Model: "gpt-5.5"}
	got := requestForOutboundPipeline(llm.APIFormatOpenAIChatCompletion, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
		return
	}
	if got == req {
		t.Fatal("requestForOutboundPipeline 应返回当前 attempt 的请求副本")
	}
	if got.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Chat Compact 副本 RequestType = %q，期望保持 Compact", got.RequestType)
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
		return
	}
	if got.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Response Compact 请求副本 RequestType = %q，期望保持 Compact", got.RequestType)
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始请求被污染为 %q，期望保持 Compact", req.RequestType)
	}
}

func TestRequestForOutboundPipelineKeepsCompactForResponseCompactChannel(t *testing.T) {
	req := &llm.Request{RequestType: llm.RequestTypeCompact, Model: "gpt-5.5"}
	got := requestForOutboundPipeline(llm.APIFormatOpenAIResponseCompact, req)
	if got == nil {
		t.Fatal("requestForOutboundPipeline returned nil")
		return
	}
	if got.RequestType != llm.RequestTypeCompact {
		t.Fatalf("OpenAI Response Compact 请求副本 RequestType = %q，期望保持 Compact", got.RequestType)
	}
	if req.RequestType != llm.RequestTypeCompact {
		t.Fatalf("原始请求被污染为 %q，期望保持 Compact", req.RequestType)
	}
}

func TestRequestForOutboundPipelineDoesNotFallbackCompactToChat(t *testing.T) {
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
		return
	}
	if got.RequestType != llm.RequestTypeCompact {
		t.Fatalf("RequestType = %q, 期望保持 Compact", got.RequestType)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("Compact 请求不应被改写为 Chat Messages，got %d", len(got.Messages))
	}

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

func compactInputMessage(role, text string) llm.Message {
	content := text
	return llm.Message{Role: role, Content: llm.MessageContent{Content: &content}}
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
		return
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
		return
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
