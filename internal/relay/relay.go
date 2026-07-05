package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/pipeline/stream"
	"github.com/looplj/axonhub/llm/streams"
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
			dbmodel.CompactStrategyResponsesManual,
			dbmodel.CompactStrategyChatManual,
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
	case dbmodel.CompactStrategyResponsesManual:
		return 1
	case dbmodel.CompactStrategyChatManual:
		return 2
	case dbmodel.CompactStrategyIncompatible:
		return 6
	}
	if channel == nil {
		return 5
	}
	switch channel.Type {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIResponseCompact:
		return 3
	default:
		return 4
	}
}

func (r *relayRun) run() {
	ctx := r.c.Request.Context()
	var lastErr error

	for {
		select {
		case <-ctx.Done():
			log.Infof("request context canceled, stopping retry")
			r.metrics.Save(ctx, false, context.Canceled, r.attempts())
			return
		default:
		}

		attempt, err := r.prepareAttempt()
		if err != nil {
			lastErr = err
			continue
		}
		if attempt == nil {
			break
		}

		written, err := attempt.run()
		if err == nil {
			r.metrics.Save(ctx, true, nil, r.attempts())
			return
		}
		if written {
			r.metrics.Save(ctx, false, err, r.attempts())
			return
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("all channels failed")
	}
	r.metrics.Save(ctx, false, lastErr, r.attempts())
	resp.Error(r.c, http.StatusBadGateway, lastErr.Error())
}

func (r *relayRun) attempts() []dbmodel.ChannelAttempt {
	attempts := make([]dbmodel.ChannelAttempt, 0)
	for _, iter := range r.iterHistory {
		attempts = append(attempts, iter.Attempts()...)
	}
	for i := range attempts {
		attempts[i].AttemptNum = i + 1
	}
	return attempts
}

func (r *relayRun) prepareAttempt() (*relayAttempt, error) {
	for {
		frame := r.currentIteratorFrame()
		if frame == nil {
			return nil, nil
		}
		item := frame.iter.Item()
		if item.Type != dbmodel.GroupItemTypeGroup {
			r.iter = frame.iter
			return r.resolveGroupItem(item, frame.iter.IsSticky(), frame.iter.StickyKeyID())
		}
		if err := r.pushNestedGroupIterator(frame, item); err != nil {
			return nil, err
		}
	}
}

func (r *relayRun) currentIteratorFrame() *relayIteratorFrame {
	for len(r.iterStack) > 0 {
		frame := r.iterStack[len(r.iterStack)-1]
		if frame.iter.Next() {
			r.iter = frame.iter
			return frame
		}
		r.iterStack = r.iterStack[:len(r.iterStack)-1]
		if len(r.iterStack) > 0 {
			r.iter = r.iterStack[len(r.iterStack)-1].iter
		}
	}
	return nil
}

func (r *relayRun) pushNestedGroupIterator(parent *relayIteratorFrame, item dbmodel.GroupItem) error {
	targetGroup, err := op.GroupGetEnabledTreeByID(item.TargetGroupID, r.c.Request.Context())
	if err != nil {
		parent.iter.SkipFor(item, false, 0, 0, fmt.Sprintf("group_%d", item.TargetGroupID), err.Error())
		return nil // Skip failed nested group and continue iteration
	}
	if len(targetGroup.Items) == 0 {
		parent.iter.SkipFor(item, false, 0, 0, targetGroup.Name, "nested group has no available channel")
		return nil
	}
	parent.iter.RedirectFor(item, 0, parent.group.Name, targetGroup.ID, targetGroup.Name, parent.depth+1, "enter nested group")
	childIter := newRelayIterator(*targetGroup, r.c.GetInt("api_key_id"), r.internalRequest, r.c.Request.Context())
	r.iterHistory = append(r.iterHistory, childIter)
	r.iterStack = append(r.iterStack, &relayIteratorFrame{
		group: *targetGroup,
		iter:  childIter,
		depth: parent.depth + 1,
	})
	return nil
}

func (r *relayRun) resolveGroupItem(
	item dbmodel.GroupItem,
	sticky bool,
	stickyKeyID int,
) (*relayAttempt, error) {
	channel, err := op.ChannelGet(item.ChannelID, r.c.Request.Context())
	if err != nil {
		log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
		msg := fmt.Sprintf("channel not found: %v", err)
		r.iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), msg)
		return nil, err
	}

	return r.buildRealAttempt(channel, item, sticky, stickyKeyID)
}

func (r *relayRun) buildRealAttempt(
	channel *dbmodel.Channel,
	item dbmodel.GroupItem,
	sticky bool,
	stickyKeyID int,
) (*relayAttempt, error) {
	if !channel.Enabled {
		r.iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
		return nil, nil
	}

	keyOptions := channel.AvailableKeysForAttempt(stickyKeyID)
	if len(keyOptions) == 0 {
		r.iter.Skip(channel.ID, 0, channel.Name, "no available key")
		return nil, nil
	}
	usedKey := keyOptions[0]

	keyRemark := cleanKeyRemark(usedKey.Remark)
	if r.iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name, keyRemark) {
		return nil, nil
	}

	baseURL := selectRuntimeBaseURL(channel)
	outAdapter, err := newOutbound(channel.Type, r.internalRequest, baseURL, usedKey.ChannelKey)
	if err != nil {
		r.iter.Skip(channel.ID, usedKey.ID, channel.Name, err.Error())
		return nil, nil
	}

	r.internalRequest.Model = item.ModelName
	r.metrics.ActualModel = item.ModelName
	r.metrics.ParamOverride = ""
	r.metrics.OutboundRequestSummary = nil

	log.Infof(
		"request model %s, mode: %d, forwarding to channel: %s model: %s "+
			"(attempt %d/%d, sticky=%t, channel_id=%d, channel_key_id=%d, sticky_key_id=%d, key_remark=%s)",
		r.metrics.RequestModel, r.group.Mode, channel.Name, item.ModelName,
		r.iter.Index()+1, r.iter.Len(), sticky,
		channel.ID, usedKey.ID, stickyKeyID, safeKeyRemark(usedKey.Remark),
	)

	return &relayAttempt{
		relayRun:   r,
		outAdapter: outAdapter,
		channel:    channel,
		groupItem:  item,
		usedKey:    usedKey,
		keyOptions: keyOptions,
		baseURL:    baseURL,
		keyRemark:  keyRemark,
	}, nil
}

// run 统一管理一次通道尝试的完整生命周期。
func (ra *relayAttempt) run() (bool, error) {
	releaseLimits, msg, ok := reserveChannelLimits(ra.channel)
	if !ok {
		ra.iter.Skip(ra.channel.ID, ra.usedKey.ID, ra.channel.Name, msg)
		return false, errors.New(msg)
	}
	defer releaseLimits()

	var lastErr error
	for {
		written, responseBody, err := ra.runWithCurrentKey()
		if err == nil || written {
			return written, err
		}
		lastErr = err
		if !ra.canRetryNextKey(err, responseBody) {
			return false, err
		}
		if !ra.switchToNextKey() {
			return false, err
		}
		log.Infof("retrying channel %s(%d) with next key=%d after key-level error: %v",
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID, lastErr)
	}
}

func (ra *relayAttempt) runWithCurrentKey() (bool, []byte, error) {

	span := ra.iter.StartAttempt(
		ra.channel.ID,
		ra.usedKey.ID,
		ra.channel.Name,
		ra.keyRemark,
	)
	ra.span = span // 保存 span 到 relayAttempt，供 writeStream 记录首 token 时间

	// 开始跟踪活跃请求
	ra.trackingID = StartTracking(
		ra.internalRequest.Model,
		ra.channel.ID,
		ra.channel.Name,
		ra.usedKey.ID,
		ra.metrics.APIKeyID,
		ra.iter.Index()+1,
	)
	defer StopTracking(ra.trackingID)

	// attempt 开始日志（第一阶段可观测性增强）
	log.Infof("attempt %d/%d start: channel=%s(%d), key=%d, sticky=%t, model=%s",
		ra.iter.Index()+1, ra.iter.Len(),
		ra.channel.Name, ra.channel.ID, ra.usedKey.ID, ra.iter.IsSticky(), ra.metrics.ActualModel)

	upstreamStatusCode, upstreamResponseBody, fwdErr := ra.forward()
	if fwdErr == nil && upstreamStatusCode == 0 {
		upstreamStatusCode = http.StatusOK
	}
	ra.usedKey.StatusCode = upstreamStatusCode
	ra.usedKey.LastUseTimeStamp = time.Now().Unix()

	if fwdErr == nil {
		recordRuntimeURLSuccess(ra.channel.ID, ra.baseURL, span.Duration())
		ra.usedKey.TotalCost += ra.metrics.Stats.InputCost + ra.metrics.Stats.OutputCost
		op.ChannelKeyUpdate(ra.usedKey)

		span.End(dbmodel.AttemptSuccess, "")
		op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:       span.Duration().Milliseconds(),
			RequestSuccess: 1,
		})
		balancer.RecordSuccess(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)

		// 获取首 token 延迟
		firstTokenDuration := time.Duration(0)
		if d, ok := span.FirstTokenDuration(); ok {
			firstTokenDuration = d
		}

		// 记录健康事件
		if smartHealthEnabled() && healthManager != nil {
			healthManager.RecordSuccess(
				ra.channel.ID,
				ra.usedKey.ID,
				ra.metrics.ActualModel,
				firstTokenDuration,
			)
		}

		// 慢成功不刷新 sticky（第二阶段健康粘性，默认关闭）
		shouldSticky := true
		firstTokenMs := int64(0)
		healthyTimeout, err := op.SettingGetInt(dbmodel.SettingKeyStickyHealthyFirstTokenTimeout)
		if err == nil && healthyTimeout > 0 {
			shouldSticky, firstTokenMs = shouldRefreshSticky(span, healthyTimeout)
			if !shouldSticky {
				log.Infof("slow success, skip sticky: channel=%s(%d), first_token=%dms, threshold=%ds",
					ra.channel.Name, ra.channel.ID, firstTokenMs, healthyTimeout)
			}
		}

		if shouldSticky {
			balancer.SetSticky(ra.metrics.APIKeyID, ra.metrics.RequestModel, ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel)
		}

		// attempt 成功日志（第一阶段可观测性增强）
		if firstTokenMs == 0 {
			firstTokenMs = firstTokenDuration.Milliseconds()
		}
		log.Infof("attempt %d/%d success: channel=%s(%d), key=%d, duration=%dms, first_token=%dms, sticky_updated=%t",
			ra.iter.Index()+1, ra.iter.Len(),
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
			span.Duration().Milliseconds(), firstTokenMs, shouldSticky)

		return false, nil, nil
	}

	if isRequestContextCanceled(ra.c.Request.Context(), fwdErr) {
		span.End(dbmodel.AttemptFailed, fwdErr.Error())
		log.Infof("attempt %d/%d canceled by request context: channel=%s(%d), key=%d, duration=%dms, error=%v",
			ra.iter.Index()+1, ra.iter.Len(),
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
			span.Duration().Milliseconds(), fwdErr)
		return ra.c.Writer.Written(), upstreamResponseBody, fwdErr
	}

	recordRuntimeURLFailure(ra.channel.ID, ra.baseURL)
	op.ChannelKeyUpdate(ra.usedKey)
	span.End(dbmodel.AttemptFailed, fwdErr.Error())
	op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
		WaitTime:      span.Duration().Milliseconds(),
		RequestFailed: 1,
	})
	if !ra.isAdaptiveFirstTokenTimeout(fwdErr) {
		balancer.RecordFailure(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
	}

	// 使用完整的上游响应体进行智能错误分类。
	// 本地首字超时不是上游 2xx 成功，即使 pipeline 已经建立了流响应，也必须按渠道级超时处理。
	classification := errorclass.Classify(upstreamStatusCode, upstreamResponseBody)
	if isFirstTokenTimeoutError(fwdErr) {
		classification = errorclass.Classification{Level: errorclass.ErrorLevelChannel, Reason: "first token timeout"}
	}

	// 获取首 token 延迟（如果有）
	firstTokenDuration := time.Duration(0)
	if d, ok := span.FirstTokenDuration(); ok {
		firstTokenDuration = d
	}

	// 记录健康事件
	if smartHealthEnabled() && healthManager != nil && !isFirstTokenTimeoutError(fwdErr) {
		healthManager.RecordError(
			ra.channel.ID,
			ra.usedKey.ID,
			ra.metrics.ActualModel,
			&classification,
			upstreamStatusCode,
			nil,
			firstTokenDuration,
		)
	}

	// attempt 失败日志（第一阶段可观测性增强 + Phase 4 error_level）
	log.Warnf("attempt %d/%d failed: channel=%s(%d), key=%d, duration=%dms, error_level=%s, error=%v",
		ra.iter.Index()+1, ra.iter.Len(),
		ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
		span.Duration().Milliseconds(), classification.Level, fwdErr)

	return ra.c.Writer.Written(), upstreamResponseBody, fmt.Errorf("channel %s failed: %v", ra.channel.Name, fwdErr)
}

func isRequestContextCanceled(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (ra *relayAttempt) canRetryNextKey(err error, upstreamResponseBody []byte) bool {
	if err == nil || ra.c.Writer.Written() || ra.keyIndex+1 >= len(ra.keyOptions) {
		return false
	}

	// 使用统一的错误分类器判断是否为 key 级错误，传入完整的上游响应体
	return errorclass.CanRetryNextKey(ra.usedKey.StatusCode, upstreamResponseBody)
}

func (ra *relayAttempt) switchToNextKey() bool {
	for ra.keyIndex+1 < len(ra.keyOptions) {
		ra.keyIndex++
		nextKey := ra.keyOptions[ra.keyIndex]
		if nextKey.ChannelKey == "" {
			continue
		}
		outAdapter, err := newOutbound(ra.channel.Type, ra.internalRequest, ra.baseURL, nextKey.ChannelKey)
		if err != nil {
			ra.iter.Skip(ra.channel.ID, nextKey.ID, ra.channel.Name, err.Error())
			continue
		}
		ra.usedKey = nextKey
		ra.keyRemark = cleanKeyRemark(nextKey.Remark)
		ra.outAdapter = outAdapter
		ra.metrics.ParamOverride = ""
		ra.metrics.OutboundRequestSummary = nil
		return true
	}
	return false
}

func shouldRefreshSticky(span *balancer.AttemptSpan, healthyTimeoutSec int) (bool, int64) {
	if healthyTimeoutSec <= 0 || span == nil {
		return true, 0
	}
	firstTokenDuration, ok := span.FirstTokenDuration()
	if !ok {
		// 非流式请求没有首 token 语义，保持既有成功即刷新粘性的行为。
		return true, 0
	}
	firstTokenMs := firstTokenDuration.Milliseconds()
	return firstTokenMs <= int64(healthyTimeoutSec)*1000, firstTokenMs
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

// errFirstTokenTimeout 标记首字超时触发的 context 取消原因，用于和客户端断开等其他取消区分。
var errFirstTokenTimeout = errors.New("first token timeout")

type firstTokenTimeoutSource int

const (
	firstTokenTimeoutDisabled firstTokenTimeoutSource = iota
	firstTokenTimeoutManual
	firstTokenTimeoutAdaptive
)

type firstTokenTimeoutConfig struct {
	Duration time.Duration
	Source   firstTokenTimeoutSource
}

type firstTokenTimeoutPhase string

const (
	firstTokenTimeoutPhaseWaitingHeaders   firstTokenTimeoutPhase = "waiting_headers"
	firstTokenTimeoutPhaseStreamFirstEvent firstTokenTimeoutPhase = "stream_first_event"
)

func (c firstTokenTimeoutConfig) Reason() string {
	switch c.Source {
	case firstTokenTimeoutManual:
		return "manual_first_token_timeout"
	case firstTokenTimeoutAdaptive:
		return "auto_first_token_timeout"
	default:
		return "first_token_timeout"
	}
}

func (c firstTokenTimeoutConfig) Error(phase firstTokenTimeoutPhase) error {
	return fmt.Errorf("%s:%s (%ds)", c.Reason(), phase, int(c.Duration.Seconds()))
}

func isFirstTokenTimeoutError(err error) bool {
	return err != nil && strings.Contains(err.Error(), errFirstTokenTimeout.Error())
}

// newFirstTokenGuard 构造首字超时守卫：
//   - 返回的 ctx 在超时且首 token 未到达时被以 errFirstTokenTimeout 取消；
//   - stop 在收到首个 token 时调用，停止计时并让后续流不再受该阈值约束；
//   - release 在本次尝试结束时调用，停止计时并释放 context 资源。
//
// 计时器回调与 stop 通过同一个 settled CAS 互斥决断：谁先成功谁生效，
// 消除"首事件已到但尚未处理时计时器误触发取消"的竞态（误切通道/截断流）。
func newFirstTokenGuard(parent context.Context, timeout time.Duration) (ctx context.Context, stop func(), release func()) {
	cctx, cancel := context.WithCancelCause(parent)
	var settled atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		if settled.CompareAndSwap(false, true) {
			cancel(errFirstTokenTimeout)
		}
	})
	stop = func() {
		// 先抢占 settled 再停表：即使计时器已在并发触发，CAS 失败也不会把已成功的首 token 误判为超时。
		settled.Store(true)
		timer.Stop()
	}
	release = func() {
		timer.Stop()
		cancel(nil)
	}
	return cctx, stop, release
}

// forward 转发请求到上游服务
func (ra *relayAttempt) forward() (int, []byte, error) {
	ctx := ra.c.Request.Context()
	if ra.internalRequest.RawRequest == nil {
		return 0, nil, fmt.Errorf("missing raw request")
	}

	httpClient, err := helper.ChannelHttpClient(ra.channel)
	if err != nil {
		log.Warnf("failed to get http client: %v", err)
		return 0, nil, err
	}

	if isCompactOpenAIChannel(ra.channel.Type, ra.internalRequest) {
		return ra.forwardCompact(ctx, httpClient)
	}

	return ra.forwardWithAdapter(ctx, httpClient, ra.outAdapter, requestForOutboundPipeline(ra.channel.Type, ra.internalRequest), needsChatToCompactResponse(ra.channel.Type, ra.internalRequest))
}

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

func smartHealthEnabled() bool {
	enabled, err := op.SettingGetBool(dbmodel.SettingKeySmartHealthEnabled)
	return err == nil && enabled
}

func (ra *relayAttempt) firstTokenTimeout() firstTokenTimeoutConfig {
	if ra.group.FirstTokenTimeOut > 0 {
		return firstTokenTimeoutConfig{
			Duration: time.Duration(ra.group.FirstTokenTimeOut) * time.Second,
			Source:   firstTokenTimeoutManual,
		}
	}
	if !smartHealthEnabled() || healthManager == nil {
		return firstTokenTimeoutConfig{}
	}
	if !healthManager.HasAdaptiveTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel) {
		return firstTokenTimeoutConfig{}
	}
	return firstTokenTimeoutConfig{
		Duration: healthManager.GetTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel),
		Source:   firstTokenTimeoutAdaptive,
	}
}

// forwardWithAdapter 用指定出站适配器执行一次完整的 pipeline 转发。
// 抽出此方法是为了支持同一渠道内的端点级降级（responses/compact → responses → chat）复用同一套转发逻辑。
// needsChatToCompact 标记是否需要将 Chat 形态响应转换为 Compact 格式（端点降级场景）。
func (ra *relayAttempt) forwardWithAdapter(
	ctx context.Context,
	httpClient *http.Client,
	outAdapter transformer.Outbound,
	outboundRequest *llm.Request,
	needsChatToCompact bool,
) (int, []byte, error) {
	// 更新活跃请求状态为等待上游（第一阶段可观测性增强）
	UpdateState(ra.trackingID, StateWaitingUpstream)

	// 首字超时覆盖"发出上游请求 → 收到首个 token"全过程，包含 pipeline.Process 内部阻塞等待响应头的阶段。
	// 出站 HTTP client 没有响应头超时，仅靠 SSE 建立后的计时器无法打断仍卡在 client.Do 等响应头的请求。
	// 只对客户端流式请求生效：非流式没有"首个 token"语义，整段响应可能合法地长于该阈值。
	fwdCtx := ctx
	stopFirstTokenGuard := func() {}
	wantStream := ra.internalRequest.Stream != nil && *ra.internalRequest.Stream
	firstTokenTimeout := ra.firstTokenTimeout()
	if firstTokenTimeout.Duration > 0 && wantStream {
		var release func()
		fwdCtx, stopFirstTokenGuard, release = newFirstTokenGuard(ctx, firstTokenTimeout.Duration)
		defer release()
	}

	relayMiddleware := &relayPipelineMiddleware{attempt: ra}

	// 如果是端点降级场景，插入 Chat 形态响应 → Compact 响应转换中间件。
	var middlewares []pipeline.Middleware
	if needsChatToCompact {
		middlewares = append(middlewares, &chatToCompactMiddleware{})
	}
	middlewares = append(middlewares, stream.EnsureUsage(), relayMiddleware)

	result, err := pipeline.NewFactory(httpclient.NewHttpClientWithClient(httpClient)).
		Pipeline(
			&parsedRequestInbound{Inbound: ra.inAdapter, request: outboundRequest},
			outAdapter,
			pipeline.WithMiddlewares(middlewares...),
			pipeline.WithEmptyResponseDetection(),
		).
		Process(fwdCtx, ra.internalRequest.RawRequest)
	if err != nil {
		// 等待响应头阶段触发首字超时：此时尚未写客户端，返回明确错误以便切换下一通道。
		if errors.Is(context.Cause(fwdCtx), errFirstTokenTimeout) {
			ra.recordFirstTokenTimeout(firstTokenTimeout)
			return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamResponseBody, firstTokenTimeout.Error(firstTokenTimeoutPhaseWaitingHeaders)
		}
		return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamResponseBody, err
	}
	if result == nil {
		return 0, nil, fmt.Errorf("empty pipeline result")
	}
	if result.Stream {
		if err := ra.writeStream(fwdCtx, stopFirstTokenGuard, firstTokenTimeout, result.EventStream); err != nil {
			return http.StatusOK, nil, err
		}
		return http.StatusOK, nil, nil
	}
	if result.Response == nil {
		return 0, nil, fmt.Errorf("empty pipeline response")
	}
	body := result.Response.Body
	statusCode := result.Response.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	contentType := "application/json"
	if result.Response.Headers != nil {
		for key, values := range result.Response.Headers {
			for _, value := range values {
				ra.c.Header(key, value)
			}
		}
		if result.Response.Headers.Get("Content-Type") != "" {
			contentType = result.Response.Headers.Get("Content-Type")
		}
	}

	// 软错误检测：HTTP 200 但内容是错误，触发重试
	if isSoftError(statusCode, body, contentType) {
		return statusCode, body, fmt.Errorf("soft error detected: upstream returned 200 but content indicates error")
	}

	ra.metrics.InternalResponse = body
	ra.c.Data(statusCode, contentType, body)
	return statusCode, body, nil
}

func (ra *relayAttempt) recordFirstTokenTimeout(timeout firstTokenTimeoutConfig) {
	if timeout.Source != firstTokenTimeoutAdaptive || timeout.Duration <= 0 || !smartHealthEnabled() || healthManager == nil {
		return
	}
	// Shadow mode: 只记录统计，不触发实际超时切换
	if healthManager.IsShadowMode() {
		healthManager.RecordShadowTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel)
		return
	}
	healthManager.RecordTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel, timeout.Duration)
}

func (ra *relayAttempt) isAdaptiveFirstTokenTimeout(err error) bool {
	if !isFirstTokenTimeoutError(err) || ra.firstTokenTimeout().Source != firstTokenTimeoutAdaptive {
		return false
	}
	// Shadow mode 下自动超时不算真正的自动超时（不影响熔断/fallback）
	if smartHealthEnabled() && healthManager != nil && healthManager.IsShadowMode() {
		return false
	}
	return true
}

func (ra *relayAttempt) applyChannelRequestOptions(outboundRequest *httpclient.Request) {
	// raw passthrough 必须先于 ParamOverride/CustomHeader：先把出站 body 还原为客户端原始字节，
	// 再让既有覆盖逻辑在其之上叠加，保持渠道配置语义不变。
	rawPassthroughApplied := ra.applyRawPassthrough(outboundRequest)
	paramOverrideApplied := false
	// ParamOverride 只覆盖 JSON 请求体；multipart 图片编辑等请求不能按 map 合并。
	if ra.channel.ParamOverride != nil && *ra.channel.ParamOverride != "" && strings.Contains(strings.ToLower(outboundRequest.Headers.Get("Content-Type")+" "+outboundRequest.ContentType), "application/json") {
		var bodyMap map[string]any
		if err := json.Unmarshal(outboundRequest.Body, &bodyMap); err != nil {
			log.Warnf("failed to unmarshal request body: %v, skipping param_override", err)
		} else {
			var override map[string]any
			if err := json.Unmarshal([]byte(*ra.channel.ParamOverride), &override); err != nil {
				log.Warnf("failed to unmarshal param_override: %v, skipping", err)
			} else {
				maps.Copy(bodyMap, override)
				modifiedBody, err := json.Marshal(bodyMap)
				if err != nil {
					log.Warnf("failed to marshal modified body: %v, skipping param_override", err)
				} else {
					outboundRequest.Body = modifiedBody
					ra.metrics.ParamOverride = *ra.channel.ParamOverride
					paramOverrideApplied = true
				}
			}
		}
	}
	// raw passthrough 生效时，出站 body 与标准化 llm.Request 不同，记录最终出站摘要供审计核对实际发送语义。
	if rawPassthroughApplied {
		ra.metrics.RecordOutboundRequestSummary(outboundRequest.Body, true, paramOverrideApplied)
	}
	for _, header := range ra.channel.CustomHeader {
		// pipeline 在 raw request middleware 前已经写入 Auth；同名敏感头保持认证配置优先，延续旧 BuildHttpRequest 的覆盖顺序。
		if outboundRequest.Headers.Get(header.HeaderKey) != "" && httpclient.IsSensitiveHeader(header.HeaderKey) {
			continue
		}
		outboundRequest.Headers.Set(header.HeaderKey, header.HeaderValue)
	}
}

// cleanKeyRemark 清洗渠道 key 备注用于持久化：去除控制字符、trim、截断到 64 rune。
// 空备注返回空字符串，便于日志层用 omitempty 省略、前端按"仅显示备注"处理。
func cleanKeyRemark(remark string) string {
	remark = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, remark))
	runes := []rune(remark)
	if len(runes) > 64 {
		return string(runes[:64]) + "..."
	}
	return remark
}

// safeKeyRemark 在 cleanKeyRemark 基础上，为终端日志把空备注展示为 "-"。
func safeKeyRemark(remark string) string {
	if cleaned := cleanKeyRemark(remark); cleaned != "" {
		return cleaned
	}
	return "-"
}

// relayPipelineMiddleware 承接 octopus 自己的通道级副作用：
// 1. 在 pipeline 发出上游请求前应用渠道参数覆盖和自定义 header；
// 2. 在上游失败时保存 HTTP 状态码和响应体，供 key 冷却、熔断和后续选路使用；
// 3. 在非流式响应转成 llm.Response 后记录 usage。
// axonhub/llm 只提供了部分函数式 middleware 构造器，错误状态码和 llm 响应 usage 这两个回调没有公开构造器，
// 所以这里保留一个很薄的结构体实现完整接口，而不是在 relay 主流程里重复 pipeline 的执行逻辑。
type relayPipelineMiddleware struct {
	pipeline.DummyMiddleware
	attempt              *relayAttempt
	upstreamStatusCode   int
	upstreamResponseBody []byte
}

func (m *relayPipelineMiddleware) Name() string {
	return "octopus_relay"
}

func (m *relayPipelineMiddleware) OnOutboundRawRequest(ctx context.Context, request *httpclient.Request) (*httpclient.Request, error) {
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	m.attempt.applyChannelRequestOptions(request)

	// Cross-format fallback: strip Codex/Responses-specific fields when falling back
	// from Responses to OpenAI Chat (e.g., nested gpt-5.5 group items under opus group).
	if m.attempt.needsCrossFormatCleaning() {
		if err := m.attempt.stripCodexFieldsFromRequestBody(request); err != nil {
			log.Warnf("failed to strip Codex fields for cross-format fallback: %v", err)
			// Non-fatal: let the request proceed; upstream will reject if fields remain.
		}
	}

	return request, nil
}

func (m *relayPipelineMiddleware) OnOutboundRawError(ctx context.Context, err error) {
	var upstreamErr *httpclient.Error
	if errors.As(err, &upstreamErr) {
		// pipeline 会把上游错误转换成统一错误返回；这里在转换前记录原始 HTTP 状态码和响应体，用于渠道 key 的后续调度决策。
		m.upstreamStatusCode = upstreamErr.StatusCode
		// 保存响应体用于智能错误分类（如 503 + model_not_found）
		m.upstreamResponseBody = upstreamErr.Body
	}
}

// needsCrossFormatCleaning detects when inbound is Codex/Responses format but
// outbound channel is OpenAI Chat, requiring Codex-specific field removal.
func (ra *relayAttempt) needsCrossFormatCleaning() bool {
	if ra.internalRequest == nil || ra.channel == nil {
		return false
	}
	inboundFormat := ra.internalRequest.APIFormat
	outboundFormat := ra.channel.Type
	// Strip Codex fields when: inbound is Responses (Codex) but outbound is OpenAI Chat.
	return (inboundFormat == llm.APIFormatOpenAIResponse || inboundFormat == llm.APIFormatOpenAIResponseCompact) &&
		outboundFormat == llm.APIFormatOpenAIChatCompletion
}

// stripCodexFieldsFromRequestBody removes Codex/Responses-specific fields that
// OpenAI Chat endpoints reject (e.g., reasoning_effort). Modifies request.Body in place.
func (ra *relayAttempt) stripCodexFieldsFromRequestBody(request *httpclient.Request) error {
	if !strings.Contains(strings.ToLower(request.ContentType+" "+request.Headers.Get("Content-Type")), "application/json") {
		return nil // Not JSON, cannot clean.
	}

	var bodyMap map[string]any
	if err := json.Unmarshal(request.Body, &bodyMap); err != nil {
		return fmt.Errorf("unmarshal request body: %w", err)
	}

	// Remove Codex-specific fields known to cause "invalid codex request" errors.
	codexFields := []string{"reasoning_effort"}
	for _, field := range codexFields {
		delete(bodyMap, field)
	}

	cleaned, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("marshal cleaned body: %w", err)
	}

	request.Body = cleaned
	return nil
}

func (m *relayPipelineMiddleware) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	if response != nil {
		// 非流式 usage 已由 outbound transformer 标准化到 llm.Response；流式 usage 在最终聚合时记录，避免重复计数。
		m.attempt.metrics.RecordUsage(response.Usage)
	}
	return response, nil
}

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

// parsedRequestInbound 让 pipeline 复用 relay 在选路前已经解析好的 llm.Request。
// 这样每次候选通道尝试只重新执行 outbound transform 和 HTTP 请求，不会重复读取或解析客户端 body。
type parsedRequestInbound struct {
	transformer.Inbound
	request *llm.Request
}

func (in *parsedRequestInbound) TransformRequest(ctx context.Context, request *httpclient.Request) (*llm.Request, error) {
	if in.request == nil {
		return nil, fmt.Errorf("missing parsed request")
	}
	// relay 已经为选路解析过请求；pipeline 入口复用该结果，避免每次通道尝试再次解析同一份 body。
	in.request.RawRequest = request
	return in.request, nil
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
