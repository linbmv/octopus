package relay

import (
	"errors"
	"fmt"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/doubao"
	"github.com/looplj/axonhub/llm/transformer/gemini"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

func newChannelOutbound(channel *dbmodel.Channel, request *llm.Request, baseURL string, key dbmodel.ChannelKey) (transformer.Outbound, error) {
	if channel == nil {
		return nil, errors.New("channel is required")
	}
	return newOutbound(channel.Type, request, baseURL, key.ChannelKey)
}

func newInbound(format llm.APIFormat) transformer.Inbound {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return openai.NewInboundTransformer()
	case llm.APIFormatOpenAIResponse:
		return responses.NewInboundTransformer()
	case llm.APIFormatOpenAIResponseCompact:
		return responses.NewCompactInboundTransformer()
	case llm.APIFormatOpenAIEmbedding:
		return openai.NewEmbeddingInboundTransformer()
	case llm.APIFormatOpenAIImageGeneration:
		return openai.NewImageGenerationInboundTransformer()
	case llm.APIFormatOpenAIImageEdit:
		return openai.NewImageEditInboundTransformer()
	case llm.APIFormatOpenAIImageVariation:
		return openai.NewImageVariationInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		return anthropic.NewInboundTransformer()
	case llm.APIFormatGeminiContents:
		return gemini.NewInboundTransformer()
	default:
		return nil
	}
}

func newOutbound(channelType llm.APIFormat, request *llm.Request, baseURL, key string) (transformer.Outbound, error) {
	requestType := llm.RequestTypeChat
	if request != nil && request.RequestType != "" {
		requestType = request.RequestType
	}

	// 将请求类型兼容性收敛到出站适配器选择处，避免 Handler 先创建适配器再用本地规则二次拦截，
	// 这样 Doubao/Gemini 在 axonhub 已经支持的 embedding/image 能力不会被项目内旧判断挡住。
	switch requestType {
	case llm.RequestTypeEmbedding:
		return newEmbeddingOutbound(channelType, baseURL, key)
	case llm.RequestTypeImage:
		return newImageOutbound(channelType, baseURL, key)
	case llm.RequestTypeCompact:
		return newCompactOutbound(channelType, baseURL, key)
	case llm.RequestTypeChat:
		return newChatOutbound(channelType, baseURL, key)
	default:
		return nil, fmt.Errorf("%s request is not supported by relay", requestType)
	}
}

func newEmbeddingOutbound(channelType llm.APIFormat, baseURL, key string) (transformer.Outbound, error) {
	switch channelType {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIEmbedding:
		return openai.NewOutboundTransformer(baseURL, key)
	case llm.APIFormatGeminiContents:
		return gemini.NewOutboundTransformer(baseURL, key)
	case dbmodel.ChannelTypeDoubao:
		return doubao.NewOutboundTransformer(baseURL, key)
	default:
		return nil, fmt.Errorf("channel type %s is not compatible with embedding request", channelType)
	}
}

func newImageOutbound(channelType llm.APIFormat, baseURL, key string) (transformer.Outbound, error) {
	switch channelType {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIImageGeneration,
		llm.APIFormatOpenAIImageEdit,
		llm.APIFormatOpenAIImageVariation:
		return openai.NewOutboundTransformer(baseURL, key)
	case llm.APIFormatGeminiContents:
		return gemini.NewOutboundTransformer(baseURL, key)
	case dbmodel.ChannelTypeDoubao:
		return doubao.NewOutboundTransformer(baseURL, key)
	default:
		return nil, fmt.Errorf("channel type %s is not compatible with image request", channelType)
	}
}

func newCompactOutbound(channelType llm.APIFormat, baseURL, key string) (transformer.Outbound, error) {
	switch channelType {
	case llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact:
		// Compact 只承接官方 /v1/responses/compact 远程压缩入口。
		return responses.NewOutboundTransformer(baseURL, key)
	default:
		// 非官方 Responses 渠道不处理 Compact；让客户端自行走本地 compact。
		return nil, fmt.Errorf("channel type %s is not compatible with compact request", channelType)
	}
}

func newChatOutbound(channelType llm.APIFormat, baseURL, key string) (transformer.Outbound, error) {
	switch channelType {
	case llm.APIFormatOpenAIChatCompletion:
		return openai.NewOutboundTransformer(baseURL, key)
	case llm.APIFormatOpenAIResponse:
		return responses.NewOutboundTransformer(baseURL, key)
	case llm.APIFormatAnthropicMessage:
		return anthropic.NewOutboundTransformer(baseURL, key)
	case llm.APIFormatGeminiContents:
		return gemini.NewOutboundTransformer(baseURL, key)
	case dbmodel.ChannelTypeDoubao:
		return doubao.NewOutboundTransformer(baseURL, key)
	default:
		return nil, fmt.Errorf("channel type %s is not compatible with chat request", channelType)
	}
}
