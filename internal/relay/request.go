package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
)

// Handler 返回处理入站请求并转发到上游服务的 Gin handler。
func Handler(inboundType llm.APIFormat) gin.HandlerFunc {
	inAdapter := newInbound(inboundType)
	return func(c *gin.Context) {
		run, err := newRelayRun(c, inboundType, inAdapter)
		if err != nil {
			return
		}
		run.run()
	}
}

func newRelayRun(c *gin.Context, inboundType llm.APIFormat, inAdapter transformer.Inbound) (*relayRun, error) {
	internalRequest, err := parseRequest(c, inboundType, inAdapter)
	if err != nil {
		return nil, err
	}

	if supportedModels := c.GetString("supported_models"); supportedModels != "" {
		if !slices.Contains(strings.Split(supportedModels, ","), internalRequest.Model) {
			err := errors.New("model not supported")
			resp.Error(c, http.StatusBadRequest, err.Error())
			return nil, err
		}
	}

	group, err := op.GroupGetEnabledTree(internalRequest.Model, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "model not found")
		return nil, err
	}

	apiKeyID := c.GetInt("api_key_id")
	iter := newRelayIterator(group, apiKeyID, internalRequest, c.Request.Context())
	if iter.Len() == 0 {
		err := errors.New("no available channel")
		resp.Error(c, http.StatusServiceUnavailable, err.Error())
		return nil, err
	}

	return &relayRun{
		c:               c,
		inAdapter:       inAdapter,
		internalRequest: internalRequest,
		metrics: &RelayMetrics{
			APIKeyID:        apiKeyID,
			RequestModel:    internalRequest.Model,
			ActualModel:     internalRequest.Model,
			StartTime:       time.Now(),
			InternalRequest: internalRequest,
		},
		iter:        iter,
		iterStack:   []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory: []*balancer.Iterator{iter},
		group:       group,
	}, nil
}

func newRelayIterator(group dbmodel.Group, apiKeyID int, request *llm.Request, ctx context.Context) *balancer.Iterator {
	candidates := nestedFallbackCandidates(group)
	if request == nil || request.RequestType != llm.RequestTypeCompact {
		requestModel := ""
		if request != nil {
			requestModel = request.Model
		}
		return balancer.NewIteratorFromCandidates(group, apiKeyID, requestModel, candidates, nil)
	}
	group.Items = candidates
	ranks := compactCandidateRanks(group, ctx)
	return balancer.NewIteratorFromCandidates(group, apiKeyID, request.Model, candidates, ranks)
}

// nestedFallbackCandidates returns group items ordered with direct channels before nested groups.
// This ensures nested groups act as fallback pools: parent group's direct channels are exhausted
// before entering any nested group, regardless of priority values.
//
// Example: if group has [DirectA(priority=100), NestedB(priority=50), DirectC(priority=30)],
// the result is [DirectA, DirectC, NestedB], NOT [NestedB, DirectC, DirectA].
func nestedFallbackCandidates(group dbmodel.Group) []dbmodel.GroupItem {
	if len(group.Items) <= 1 {
		return group.Items
	}
	directItems := make([]dbmodel.GroupItem, 0, len(group.Items))
	nestedItems := make([]dbmodel.GroupItem, 0)
	for _, item := range group.Items {
		if item.Type != dbmodel.GroupItemTypeGroup {
			directItems = append(directItems, item)
		} else {
			nestedItems = append(nestedItems, item)
		}
	}
	if len(nestedItems) == 0 || len(directItems) == 0 {
		return balancer.GetBalancer(group.Mode).Candidates(group.Items)
	}
	ordered := balancer.GetBalancer(group.Mode).Candidates(directItems)
	ordered = append(ordered, balancer.GetBalancer(group.Mode).Candidates(nestedItems)...)
	return ordered
}

func compactCandidateRanks(group dbmodel.Group, ctx context.Context) map[int]int {
	ranks := make(map[int]int, len(group.Items))
	for _, item := range group.Items {
		if item.ID == 0 || item.ChannelID == 0 {
			continue
		}
		switch item.CompactStrategy {
		case dbmodel.CompactStrategyOfficial,
			dbmodel.CompactStrategyIncompatible:
			ranks[item.ID] = compactGroupItemRank(item, nil)
			continue
		}
		// Unknown strategies still need channel.Type to distinguish OpenAI-compatible
		// candidates, but op.ChannelGet is a pure in-memory cache lookup.
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			continue
		}
		ranks[item.ID] = compactGroupItemRank(item, channel)
	}
	return ranks
}

func compactGroupItemRank(item dbmodel.GroupItem, channel *dbmodel.Channel) int {
	switch item.CompactStrategy {
	case dbmodel.CompactStrategyOfficial:
		return 0
	case dbmodel.CompactStrategyIncompatible:
		return 6
	}
	if channel == nil {
		return 5
	}
	switch channel.Type {
	case llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact:
		return 3
	default:
		return 4
	}
}

// parseRequest 解析并验证入站请求
func parseRequest(c *gin.Context, inboundType llm.APIFormat, inAdapter transformer.Inbound) (*llm.Request, error) {
	if inAdapter == nil {
		err := fmt.Errorf("unsupported inbound type: %s", inboundType)
		resp.Error(c, http.StatusBadRequest, err.Error())
		return nil, err
	}

	httpRequest, err := httpclient.ReadHTTPRequest(c.Request)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return nil, err
	}

	internalRequest, err := inAdapter.TransformRequest(c.Request.Context(), httpRequest)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, transformer.ErrInvalidRequest) {
			statusCode = http.StatusBadRequest
		}
		resp.Error(c, statusCode, err.Error())
		return nil, err
	}
	if internalRequest.RawRequest == nil {
		internalRequest.RawRequest = httpRequest
	}

	return internalRequest, nil
}
