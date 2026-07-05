package relay

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

func isCompactOpenAIChannel(channelType llm.APIFormat, request *llm.Request) bool {
	if request == nil || request.RequestType != llm.RequestTypeCompact {
		return false
	}
	switch channelType {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact:
		return true
	default:
		return false
	}
}

func (ra *relayAttempt) forwardCompact(ctx context.Context, httpClient *http.Client) (int, []byte, error) {
	cached, hasCached := ra.cachedCompactStrategy()
	strategies := compactStrategyOrder(ra.channel.Type, cached, hasCached)
	if len(strategies) == 0 {
		return 0, nil, fmt.Errorf("channel type %s is not compatible with %s request", ra.channel.Type, llm.RequestTypeCompact)
	}

	var lastStatusCode int
	var lastResponseBody []byte
	var lastErr error
	for _, strategy := range strategies {
		outAdapter, outboundRequest, needsChatToCompact, err := ra.compactAttempt(strategy)
		if err != nil {
			return lastStatusCode, lastResponseBody, err
		}

		log.Infof("compact route: channel=%s(%d), strategy=%s", ra.channel.Name, ra.channel.ID, strategy)
		statusCode, responseBody, fwdErr := ra.forwardWithAdapter(ctx, httpClient, outAdapter, outboundRequest, needsChatToCompact)
		if fwdErr == nil {
			ra.rememberCompactStrategy(ctx, strategy)
			return statusCode, responseBody, nil
		}

		lastStatusCode = statusCode
		lastResponseBody = responseBody
		lastErr = fwdErr
		if !ra.canTryNextCompactStrategy(strategy, fwdErr) {
			return statusCode, responseBody, fwdErr
		}
	}

	return lastStatusCode, lastResponseBody, lastErr
}

func (ra *relayAttempt) compactAttempt(strategy compactStrategy) (transformer.Outbound, *llm.Request, bool, error) {
	switch strategy {
	case compactStrategyOfficial:
		return ra.outAdapter, compactOfficialRequest(ra.internalRequest), false, nil
	case compactStrategyResponsesManual:
		return ra.outAdapter, compactResponsesFallbackRequest(ra.internalRequest), true, nil
	case compactStrategyChatManual:
		baseURL := ra.baseURL
		if baseURL == "" {
			baseURL = ra.channel.GetBaseUrl()
		}
		if ra.channel.Type == llm.APIFormatOpenAIChatCompletion {
			return ra.outAdapter, compactChatFallbackRequest(ra.internalRequest), true, nil
		}
		chatAdapter, err := newOutbound(llm.APIFormatOpenAIChatCompletion, ra.internalRequest, baseURL, ra.usedKey.ChannelKey)
		if err != nil {
			log.Warnf("compact endpoint downgrade: build chat outbound failed: %v", err)
			return nil, nil, false, err
		}
		return chatAdapter, compactChatFallbackRequest(ra.internalRequest), true, nil
	default:
		return nil, nil, false, fmt.Errorf("unknown compact strategy: %s", strategy)
	}
}

func (ra *relayAttempt) canTryNextCompactStrategy(strategy compactStrategy, fwdErr error) bool {
	switch strategy {
	case compactStrategyOfficial:
		return ra.canFallbackOfficialCompactToManual(fwdErr)
	case compactStrategyResponsesManual:
		return ra.canDowngradeCompactResponsesFallback(fwdErr)
	default:
		return false
	}
}

// canDowngradeCompactEndpoint 判断当前失败是否满足"同渠道 Compact 端点降级"的全部条件。
func (ra *relayAttempt) canDowngradeCompactEndpoint(fwdErr error) bool {
	if ra.internalRequest.RequestType != llm.RequestTypeCompact {
		return false
	}
	switch ra.channel.Type {
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
	default:
		return false
	}
	// 客户端已开始接收响应（流式已写出）时不能重发，否则会产生重复输出。
	if ra.c.Writer.Written() {
		return false
	}
	return isEndpointUnsupportedError(fwdErr)
}

// canFallbackOfficialCompactToManual 判断官方 /responses/compact 失败后是否允许同渠道改用手动压缩。
func (ra *relayAttempt) canFallbackOfficialCompactToManual(fwdErr error) bool {
	if ra.internalRequest.RequestType != llm.RequestTypeCompact {
		return false
	}
	switch ra.channel.Type {
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
	default:
		return false
	}
	if ra.c.Writer.Written() {
		return false
	}
	return isEndpointUnsupportedError(fwdErr) ||
		isCompactResponsesFallbackIncompatibleError(fwdErr) ||
		isCompactManualFallbackError(fwdErr)
}

// canDowngradeCompactResponsesFallback 判断普通 /responses fallback 失败后是否还能继续降到 Chat。
func (ra *relayAttempt) canDowngradeCompactResponsesFallback(fwdErr error) bool {
	if ra.canDowngradeCompactEndpoint(fwdErr) {
		return true
	}
	return false
}

func needsChatToCompactResponse(channelType llm.APIFormat, request *llm.Request) bool {
	return channelType == llm.APIFormatOpenAIChatCompletion && request != nil && request.RequestType == llm.RequestTypeCompact
}
