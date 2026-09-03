package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/bestruirui/octopus/internal/routingstate"
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
			run.initialResponseTimeoutSeconds,
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
	routingSnapshot := routingstate.Current()
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
	sessionID := relaySessionID(c, internalRequest)
	iter := newRelayIteratorWithSessionAndBaseURLs(group, apiKeyID, internalRequest, c.Request.Context(), selectedBaseURLs, sessionID)
	if iter.Len() == 0 {
		err := errors.New("no available channel")
		respondRelayError(c, http.StatusServiceUnavailable, err)
		return nil, err
	}

	relayConfig := conf.Current().Relay
	run := &relayRun{
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
		iter:                iter,
		iterStack:           []*relayIteratorFrame{{group: group, iter: iter, depth: 0}},
		iterHistory:         []*balancer.Iterator{iter},
		group:               group,
		selectedBaseURLs:    selectedBaseURLs,
		sessionID:           sessionID,
		maxUpstreamAttempts: relayConfig.MaxUpstreamAttempts,
		initialResponseTimeoutSeconds: initialResponseBudgetSeconds(
			relayConfig.NonStreamTimeoutSeconds,
			relayConfig.InitialResponseTimeoutSeconds,
			group,
			c.Request.Context(),
		),
		streamFirstEventBudget: time.Duration(initialResponseBudgetSeconds(
			relayConfig.StreamFirstEventBudgetSeconds,
			relayConfig.InitialResponseTimeoutSeconds,
			group,
			c.Request.Context(),
		)) * time.Second,
		routingSnapshot: routingSnapshot,
		failoverState:   newRequestFailoverState(),
	}
	run.attachIteratorTimeline(iter)
	return run, nil
}

// initialResponseBudgetSeconds keeps the process-wide request deadline at the
// normal 120-second ceiling unless an enabled candidate explicitly asks for a
// channel exception. The per-attempt guards still enforce the normal ceiling
// for every channel that did not opt in.
func initialResponseBudgetSeconds(configuredSeconds, ceilingSeconds int, group dbmodel.Group, ctx context.Context) int {
	configured := boundedInitialResponseTimeoutSeconds(configuredSeconds, ceilingSeconds)
	if exception := maxChannelInitialResponseTimeoutSeconds(group, ctx); exception > configured {
		return exception
	}
	return configured
}

func maxChannelInitialResponseTimeoutSeconds(group dbmodel.Group, ctx context.Context) int {
	maxSeconds := hardMaxInitialResponseTimeoutSeconds
	visited := make(map[int]struct{})
	var walk func(dbmodel.Group)
	walk = func(current dbmodel.Group) {
		for _, item := range current.Items {
			if item.Type == dbmodel.GroupItemTypeGroup {
				if item.TargetGroupID <= 0 {
					continue
				}
				if _, seen := visited[item.TargetGroupID]; seen {
					continue
				}
				visited[item.TargetGroupID] = struct{}{}
				nested, err := op.GroupGetEnabledTreeByID(item.TargetGroupID, ctx)
				if err == nil && nested != nil {
					walk(*nested)
				}
				delete(visited, item.TargetGroupID)
				continue
			}
			if item.ChannelID <= 0 {
				continue
			}
			channel, err := op.ChannelGet(item.ChannelID, ctx)
			if err != nil {
				continue
			}
			if seconds := channelFirstTokenTimeoutExceptionSeconds(channel); seconds > maxSeconds {
				maxSeconds = seconds
			}
		}
	}
	walk(group)
	return maxSeconds
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
	return newRelayIteratorWithSessionAndBaseURLs(group, apiKeyID, request, ctx, selectedBaseURLs, "")
}

func newRelayIteratorWithSessionAndBaseURLs(
	group dbmodel.Group,
	apiKeyID int,
	request *llm.Request,
	ctx context.Context,
	selectedBaseURLs map[int]string,
	sessionID string,
) *balancer.Iterator {
	candidates := nestedFallbackCandidates(group)
	policyProfiles := candidatePolicyProfiles(candidates, ctx)
	if request == nil || request.RequestType != llm.RequestTypeCompact {
		requestModel := ""
		if request != nil {
			requestModel = request.Model
		}
		ranks := capabilityCandidateRanks(candidates, request, ctx, selectedBaseURLs)
		return balancer.NewIteratorFromCandidatesWithSession(group, apiKeyID, requestModel, sessionID, candidates, ranks, policyProfiles)
	}
	group.Items = candidates
	ranks := compactCandidateRanks(group, ctx)
	return balancer.NewIteratorFromCandidatesWithSession(group, apiKeyID, request.Model, sessionID, candidates, ranks, policyProfiles)
}

func candidatePolicyProfiles(candidates []dbmodel.GroupItem, ctx context.Context) map[int]string {
	profiles := make(map[int]string)
	for _, item := range candidates {
		if item.ID == 0 || item.ChannelID <= 0 {
			continue
		}
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err == nil && channel != nil {
			profiles[item.ID] = string(channel.PolicyProfile)
		}
	}
	return profiles
}

func relaySessionID(c *gin.Context, request *llm.Request) string {
	if request == nil {
		return ""
	}
	var raw string
	for _, key := range []string{"session_id", "conversation_id", "thread_id"} {
		if value := validSessionIdentity(request.Metadata[key]); value != "" {
			raw = value
			break
		}
	}
	if raw == "" && request.PromptCacheKey != nil {
		raw = validSessionIdentity(*request.PromptCacheKey)
	}
	if raw == "" && request.User != nil {
		raw = validSessionIdentity(*request.User)
	}
	if raw == "" && c != nil {
		raw = validSessionIdentity(c.GetHeader("X-Octopus-Session-ID"))
	}
	if raw == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func validSessionIdentity(raw string) string {
	if raw == "" || len(raw) > 256 || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	return strings.TrimSpace(raw)
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
	return candidateRanksFromTree(candidates, ctx, op.GroupGetEnabledTreeByID, 1, func(item dbmodel.GroupItem) int {
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			return 1
		}
		endpoint := selectedBaseURLForChannel(channel, selectedBaseURLs)
		return op.CapabilityChannelRank(ctx, channel, item.ModelName, required, endpoint)
	})
}

type enabledGroupResolver func(int, context.Context) (*dbmodel.Group, error)
type groupLeafRanker func(dbmodel.GroupItem) int

// candidateRanksFromTree assigns every parent candidate a rank. A nested group
// inherits the best rank among its enabled descendant channels, so capability
// ordering cannot silently demote the group merely because the parent item has
// no ChannelID of its own.
func candidateRanksFromTree(
	candidates []dbmodel.GroupItem,
	ctx context.Context,
	resolve enabledGroupResolver,
	unknownRank int,
	rankLeaf groupLeafRanker,
) map[int]int {
	if len(candidates) == 0 || resolve == nil || rankLeaf == nil {
		return nil
	}
	ranks := make(map[int]int, len(candidates))
	for _, item := range candidates {
		if item.ID == 0 {
			continue
		}
		ranks[item.ID] = groupItemTreeRank(item, ctx, resolve, unknownRank, rankLeaf, make(map[int]struct{}))
	}
	return ranks
}

func groupItemTreeRank(
	item dbmodel.GroupItem,
	ctx context.Context,
	resolve enabledGroupResolver,
	unknownRank int,
	rankLeaf groupLeafRanker,
	visited map[int]struct{},
) int {
	if item.Type != dbmodel.GroupItemTypeGroup {
		if item.ChannelID <= 0 {
			return unknownRank
		}
		return rankLeaf(item)
	}
	if item.TargetGroupID <= 0 {
		return unknownRank
	}
	if _, exists := visited[item.TargetGroupID]; exists {
		return unknownRank
	}
	visited[item.TargetGroupID] = struct{}{}
	defer delete(visited, item.TargetGroupID)

	group, err := resolve(item.TargetGroupID, ctx)
	if err != nil || group == nil || len(group.Items) == 0 {
		return unknownRank
	}
	best := unknownRank
	found := false
	for _, child := range group.Items {
		rank := groupItemTreeRank(child, ctx, resolve, unknownRank, rankLeaf, visited)
		if !found || rank < best {
			best = rank
			found = true
		}
	}
	if !found {
		return unknownRank
	}
	return best
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

func baseURLCandidatesForChannel(channel *dbmodel.Channel, selected map[int]string) []string {
	if channel == nil {
		return nil
	}
	candidates := runtimeBaseURLCandidates(channel)
	if len(candidates) == 0 {
		return nil
	}
	preferred := ""
	if selected != nil {
		preferred = selected[channel.ID]
	}
	if preferred == "" || candidates[0] == preferred {
		return candidates
	}
	for i, candidate := range candidates {
		if candidate != preferred {
			continue
		}
		copy(candidates[1:i+1], candidates[:i])
		candidates[0] = preferred
		break
	}
	return candidates
}

// nestedFallbackCandidates returns group items ordered by the parent group's
// balancer without treating nested groups as a separate fallback partition.
// Nested group items therefore participate in the same priority/weight/random
// ordering as direct channel items; once selected, the nested group is expanded
// into its own iterator and applies its own balancing policy.
func nestedFallbackCandidates(group dbmodel.Group) []dbmodel.GroupItem {
	if len(group.Items) == 0 {
		return nil
	}
	return balancer.GetBalancer(group.Mode).Candidates(group.Items)
}

func compactCandidateRanks(group dbmodel.Group, ctx context.Context) map[int]int {
	return candidateRanksFromTree(group.Items, ctx, op.GroupGetEnabledTreeByID, 5, func(item dbmodel.GroupItem) int {
		switch item.CompactStrategy {
		case dbmodel.CompactStrategyOfficial,
			dbmodel.CompactStrategyIncompatible:
			return compactGroupItemRank(item, nil)
		}
		// Unknown strategies still need channel.Type to distinguish OpenAI-compatible
		// candidates, but op.ChannelGet is a pure in-memory cache lookup.
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			return 5
		}
		return compactGroupItemRank(item, channel)
	})
}

func compactGroupItemRank(item dbmodel.GroupItem, channel *dbmodel.Channel) int {
	switch item.CompactStrategy {
	case dbmodel.CompactStrategyOfficial:
		return 0
	case dbmodel.CompactStrategyIncompatible:
		// 已确认不兼容属于硬不可用：排序时无视用户 Priority 直接垫底。
		return balancer.HardUnusableRank + 6
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
