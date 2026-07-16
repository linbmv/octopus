package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/tracing"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var (
	errNonStreamRequestTimeout         = errors.New("non-streaming upstream request timed out")
	errRelayRequestBodyTooLarge        = errors.New("relay request body too large")
	errRelayContentEncodingUnsupported = errors.New("relay request content encoding is unsupported")
)

// Handler 返回处理入站请求并转发到上游服务的 Gin handler。
func Handler(inboundType llm.APIFormat) gin.HandlerFunc {
	inAdapter := newInbound(inboundType)
	return func(c *gin.Context) {
		run, err := newRelayRun(c, inboundType, inAdapter)
		if err != nil {
			return
		}
		requestCtx, cancel := newRelayRequestContext(
			c.Request.Context(),
			run.internalRequest,
			conf.Current().Relay.NonStreamTimeoutSeconds,
		)
		defer cancel()
		ctx, span := tracing.Tracer().Start(requestCtx, "relay.request")
		defer span.End()
		span.SetAttributes(
			attribute.String("gen_ai.request.model", run.internalRequest.Model),
			attribute.String("gen_ai.operation.name", string(inboundType)),
		)
		c.Request = c.Request.WithContext(ctx)
		run.run()
		if c.Writer.Status() >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(c.Writer.Status()))
		}
	}
}

// newRelayRequestContext applies one deadline to the entire non-streaming
// upstream lifecycle. Keeping it outside individual attempts prevents every
// retry/key/channel from receiving a fresh budget. Streaming requests retain
// their phase-specific connection/first-token/idle guards instead.
func newRelayRequestContext(parent context.Context, request *llm.Request, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeoutSeconds <= 0 || request == nil || (request.Stream != nil && *request.Stream) {
		return parent, func() {}
	}
	return context.WithTimeoutCause(parent, time.Duration(timeoutSeconds)*time.Second, errNonStreamRequestTimeout)
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
	selectedBaseURLs := make(map[int]string)
	iter := newRelayIteratorWithBaseURLs(group, apiKeyID, internalRequest, c.Request.Context(), selectedBaseURLs)
	if iter.Len() == 0 {
		err := errors.New("no available channel")
		respondRelayError(c, http.StatusServiceUnavailable, err)
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
		iter:             iter,
		iterStack:        []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:      []*balancer.Iterator{iter},
		group:            group,
		selectedBaseURLs: selectedBaseURLs,
	}, nil
}

func newRelayIterator(group dbmodel.Group, apiKeyID int, request *llm.Request, ctx context.Context) *balancer.Iterator {
	return newRelayIteratorWithBaseURLs(group, apiKeyID, request, ctx, make(map[int]string))
}

func newRelayIteratorWithBaseURLs(
	group dbmodel.Group,
	apiKeyID int,
	request *llm.Request,
	ctx context.Context,
	selectedBaseURLs map[int]string,
) *balancer.Iterator {
	candidates := nestedFallbackCandidates(group)
	if request == nil || request.RequestType != llm.RequestTypeCompact {
		requestModel := ""
		if request != nil {
			requestModel = request.Model
		}
		ranks := capabilityCandidateRanks(candidates, request, ctx, selectedBaseURLs)
		return balancer.NewIteratorFromCandidates(group, apiKeyID, requestModel, candidates, ranks)
	}
	group.Items = candidates
	ranks := compactCandidateRanks(group, ctx)
	return balancer.NewIteratorFromCandidates(group, apiKeyID, request.Model, candidates, ranks)
}

func capabilityCandidateRanks(
	candidates []dbmodel.GroupItem,
	request *llm.Request,
	ctx context.Context,
	selectedBaseURLs map[int]string,
) map[int]int {
	if request == nil {
		return nil
	}
	required := dbmodel.RequiredCapabilities(request)
	ranks := make(map[int]int, len(candidates))
	for _, item := range candidates {
		if item.ID == 0 || item.ChannelID == 0 || item.Type == dbmodel.GroupItemTypeGroup {
			continue
		}
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			continue
		}
		endpoint := selectedBaseURLForChannel(channel, selectedBaseURLs)
		ranks[item.ID] = op.CapabilityChannelRank(ctx, channel, item.ModelName, required, endpoint)
	}
	return ranks
}

func selectedBaseURLForChannel(channel *dbmodel.Channel, selected map[int]string) string {
	if channel == nil {
		return ""
	}
	if endpoint, ok := selected[channel.ID]; ok {
		return endpoint
	}
	endpoint := selectRuntimeBaseURL(channel)
	if selected != nil {
		selected[channel.ID] = endpoint
	}
	return endpoint
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

	httpRequest, err := readRelayHTTPRequest(c, inboundType)
	if err != nil {
		if errors.Is(err, errRelayRequestBodyTooLarge) {
			resp.ErrorWithCode(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
			return nil, err
		}
		if errors.Is(err, errRelayContentEncodingUnsupported) {
			resp.ErrorWithCode(c, http.StatusUnsupportedMediaType, "REQUEST_CONTENT_ENCODING_UNSUPPORTED", "compressed request bodies are not supported")
			return nil, err
		}
		resp.Error(c, http.StatusBadRequest, "failed to read request body")
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

func readRelayHTTPRequest(c *gin.Context, inboundType llm.APIFormat) (*httpclient.Request, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("HTTP request is missing")
	}
	encoding := strings.TrimSpace(c.GetHeader("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, fmt.Errorf("%w: %s", errRelayContentEncodingUnsupported, encoding)
	}

	config := conf.Current().Relay
	maxBytes := config.MaxJSONRequestBytes
	if inboundType == llm.APIFormatOpenAIImageEdit || inboundType == llm.APIFormatOpenAIImageVariation {
		maxBytes = config.MaxImageRequestBytes
	}
	if maxBytes <= 0 || c.Request.ContentLength > maxBytes {
		return nil, errRelayRequestBodyTooLarge
	}
	body, buffered := bodylimit.BufferedBody(c.Request.Context())
	if buffered {
		if int64(len(body)) > maxBytes {
			return nil, errRelayRequestBodyTooLarge
		}
	} else {
		var err error
		body, err = bodylimit.ReadAll(c.Request.Body, maxBytes)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.Is(err, bodylimit.ErrTooLarge) || errors.As(err, &maxBytesErr) {
				return nil, fmt.Errorf("%w: %v", errRelayRequestBodyTooLarge, err)
			}
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}
	c.Request.ContentLength = int64(len(body))

	return &httpclient.Request{
		Method:     c.Request.Method,
		URL:        c.Request.URL.String(),
		Path:       c.Request.URL.Path,
		Query:      c.Request.URL.Query(),
		Headers:    c.Request.Header,
		Body:       body,
		Auth:       &httpclient.AuthConfig{},
		RequestID:  c.GetString("request_id"),
		ClientIP:   c.ClientIP(),
		RawRequest: c.Request,
	}, nil
}
