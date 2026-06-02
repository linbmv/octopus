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
