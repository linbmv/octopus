package relay

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
)

func isCompactOpenAIChannel(channelType llm.APIFormat, request *llm.Request) bool {
	if request == nil || request.RequestType != llm.RequestTypeCompact {
		return false
	}
	switch channelType {
	case llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact:
		return true
	default:
		return false
	}
}

func (ra *relayAttempt) forwardCompact(ctx context.Context, httpClient *http.Client) (int, http.Header, []byte, error) {
	strategies := compactStrategyOrder(ra.channel.Type)
	if len(strategies) == 0 {
		return 0, nil, nil, fmt.Errorf("channel type %s is not compatible with %s request", ra.channel.Type, llm.RequestTypeCompact)
	}

	strategy := strategies[0]
	log.Infof("compact route: channel=%s(%d), strategy=%s", ra.channel.Name, ra.channel.ID, strategy)
	statusCode, headers, responseBody, err := ra.forwardWithAdapter(ctx, httpClient, ra.outAdapter, compactOfficialRequest(ra.internalRequest))
	if err == nil {
		ra.rememberCompactStrategy(ctx, strategy)
	}
	return statusCode, headers, responseBody, err
}
