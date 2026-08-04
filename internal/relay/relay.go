package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/tracing"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/pipeline/stream"
	"github.com/looplj/axonhub/llm/transformer"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// run 统一管理一次通道尝试的完整生命周期。
func (ra *relayAttempt) run() (bool, error) {
	releaseLimits, msg, ok := reserveChannelLimits(ra.channel)
	if !ok {
		// 该 key 可能刚被授予半开试探名额；未发请求即放弃必须归还，
		// 否则 RPM/并发饱和会让半开态一直无有效试探。
		balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		ra.iter.Skip(ra.channel.ID, ra.usedKey.ID, ra.channel.Name, msg)
		ra.exhaustRequestCandidate(requestFailureChannel)
		return false, errors.New(msg)
	}
	defer releaseLimits()

	var lastErr error
	for {
		if ra.streamFirstEventBudget > 0 && ra.remainingStreamFirstEventBudget() <= 0 {
			balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
			return false, newTerminalRelayError(http.StatusGatewayTimeout, errStreamFirstEventBudget)
		}
		if !ra.reserveUpstreamAttempt() {
			balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
			return false, newTerminalRelayError(http.StatusServiceUnavailable, errUpstreamAttemptBudget)
		}
		written, responseHeaders, responseBody, err := ra.runWithCurrentKey()
		if err == nil || written {
			return written, err
		}
		if errors.Is(err, errRoutingConfigChanged) {
			ra.exhaustRequestCandidate(requestFailureChannel)
			return false, err
		}
		if isRequestContextCanceled(ra.c.Request.Context(), err) {
			return false, err
		}
		lastErr = err
		if ra.canRetryNextBaseURL(err, responseHeaders, responseBody) {
			ra.exhaustRequestCandidate(requestFailureCandidate)
			if ra.switchToNextBaseURL() {
				log.Infof("retrying channel %s(%d) with next base URL after endpoint-level error: %v",
					ra.channel.Name, ra.channel.ID, lastErr)
				continue
			}
		}
		if !ra.canRetryNextKey(err, responseHeaders, responseBody) {
			decision := ra.decideError(ra.usedKey.StatusCode, responseHeaders, responseBody, err)
			scope := requestFailureChannel
			if decision.Action == ErrorActionRetryKey || decision.Classification.Level == errorclass.ErrorLevelKey {
				scope = requestFailureKey
			} else if decision.Action == ErrorActionReturnClient || decision.Action == ErrorActionNone {
				scope = requestFailureCandidate
			}
			ra.exhaustRequestCandidate(scope)
			return false, err
		}
		ra.exhaustRequestCandidate(requestFailureKey)
		if !ra.switchToNextKey() {
			return false, err
		}
		log.Infof("retrying channel %s(%d) with next key=%d after key-level error: %v",
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID, lastErr)
	}
}

func (ra *relayAttempt) runWithCurrentKey() (bool, http.Header, []byte, error) {
	if msg, ok := reserveChannelRPM(ra.channel); !ok {
		balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		ra.iter.Skip(ra.channel.ID, ra.usedKey.ID, ra.channel.Name, msg)
		return false, nil, nil, &channelRequestLimitError{message: msg}
	}

	ctx := log.WithChannelID(ra.c.Request.Context(), ra.channel.ID)
	ctx, upstreamSpan := tracing.Tracer().Start(ctx, "relay.upstream",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int("octopus.channel.id", ra.channel.ID),
			attribute.String("octopus.channel.name", ra.channel.Name),
			attribute.Int("octopus.channel_key.id", ra.usedKey.ID),
			attribute.String("gen_ai.request.model", ra.metrics.ActualModel),
		),
	)
	defer upstreamSpan.End()
	ra.c.Request = ra.c.Request.WithContext(ctx)

	span := ra.iter.StartAttempt(
		ra.channel.ID,
		ra.usedKey.ID,
		ra.channel.Name,
		ra.keyRemark,
	)
	span.SetRoutingMetadata(safeRuntimeURL(ra.baseURL), ra.attemptAction, false, ra.selectionReason)
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

	upstreamStatusCode, upstreamHeaders, upstreamResponseBody, fwdErr := ra.forward()
	ra.recordStreamFirstEventWait(span, fwdErr)
	if errors.Is(fwdErr, errRoutingConfigChanged) {
		markRoutingRefreshAttempt(span, ra.channel, ra.usedKey, ra.internalRequest.Model, safeRuntimeURL(ra.baseURL))
		log.Infof("attempt interrupted by routing configuration change: channel=%s(%d), key=%d, duration=%dms",
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID, span.Duration().Milliseconds())
		return false, upstreamHeaders, upstreamResponseBody, errRoutingConfigChanged
	}
	if fwdErr != nil && isNonStreamRequestTimeout(ra.c.Request.Context()) {
		fwdErr = fmt.Errorf("%w: %v", errNonStreamRequestTimeout, fwdErr)
	}
	if fwdErr == nil && upstreamStatusCode == 0 {
		upstreamStatusCode = http.StatusOK
	}
	ra.usedKey.StatusCode = upstreamStatusCode
	now := time.Now()
	ra.usedKey.LastUseTimeStamp = now.Unix()
	if upstreamStatusCode == http.StatusTooManyRequests {
		cooldown := retryAfterDuration(upstreamHeaders, now, defaultRateLimitCooldown)
		ra.usedKey.RetryAfterUntil = now.Add(cooldown).Unix()
	} else {
		ra.usedKey.RetryAfterUntil = 0
	}

	if fwdErr == nil {
		ra.recordSuccessfulChannelBaseline(ctx, upstreamStatusCode, upstreamHeaders)
		upstreamSpan.SetAttributes(attribute.Int("http.response.status_code", upstreamStatusCode))
		urlLatency := ra.responseHeaderDuration
		if firstTokenDuration, ok := span.FirstTokenDuration(); ok {
			urlLatency = firstTokenDuration
		}
		if urlLatency <= 0 {
			urlLatency = span.Duration()
		}
		recordRuntimeURLSuccess(ra.channel.ID, ra.baseURL, urlLatency)
		ra.usedKey.TotalCost += ra.metrics.Stats.InputCost + ra.metrics.Stats.OutputCost
		if updateErr := op.ChannelKeyUpdate(ra.usedKey); updateErr != nil {
			log.WithContext(ctx).Warnw("failed to update channel key runtime state",
				"channel_id", ra.channel.ID, "channel_key_id", ra.usedKey.ID, "error", updateErr)
		}

		span.End(dbmodel.AttemptSuccess, "")
		if statsErr := op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:       span.Duration().Milliseconds(),
			RequestSuccess: 1,
		}); statsErr != nil {
			log.WithContext(ctx).Warnw("failed to update successful channel attempt statistics",
				"channel_id", ra.channel.ID, "error", statsErr)
		}
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
			balancer.SetStickyForSession(ra.metrics.APIKeyID, ra.metrics.RequestModel, ra.sessionID, ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel)
		}

		// attempt 成功日志（第一阶段可观测性增强）
		if firstTokenMs == 0 {
			firstTokenMs = firstTokenDuration.Milliseconds()
		}
		log.Infof("attempt %d/%d success: channel=%s(%d), key=%d, duration=%dms, first_token=%dms, sticky_updated=%t",
			ra.iter.Index()+1, ra.iter.Len(),
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
			span.Duration().Milliseconds(), firstTokenMs, shouldSticky)

		return false, upstreamHeaders, nil, nil
	}

	if isRequestContextCanceled(ra.c.Request.Context(), fwdErr) {
		upstreamSpan.RecordError(fwdErr)
		upstreamSpan.SetStatus(codes.Error, "request canceled")
		msg := "request context canceled"
		if ra.c.Writer.Written() {
			msg = "client disconnected"
		}
		// 客户端取消不能证明通道好坏，但若本次是半开试探必须归还名额：
		// 这是"模型冻结后长期不恢复"的最常见触发路径。
		balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		span.SetRoutingMetadata(safeRuntimeURL(ra.baseURL), "client_cancel", false, ra.selectionReason)
		span.End(dbmodel.AttemptClientCancel, msg)
		log.Infof("attempt %d/%d canceled by request context: channel=%s(%d), key=%d, duration=%dms, msg=%s, error=%v",
			ra.iter.Index()+1, ra.iter.Len(),
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
			span.Duration().Milliseconds(), msg, fwdErr)
		return ra.c.Writer.Written(), upstreamHeaders, upstreamResponseBody, fwdErr
	}

	if updateErr := op.ChannelKeyUpdate(ra.usedKey); updateErr != nil {
		log.WithContext(ctx).Warnw("failed to update channel key runtime state",
			"channel_id", ra.channel.ID, "channel_key_id", ra.usedKey.ID, "error", updateErr)
	}
	// 使用完整的上游响应体进行智能错误决策。
	decision := ra.decideError(upstreamStatusCode, upstreamHeaders, upstreamResponseBody, fwdErr)
	transportErr := fwdErr
	if upstreamStatusCode > 0 {
		// HTTP and soft/SSE failures must be diagnosed from the same response
		// evidence as ClassifyWithHeaders, not mislabeled as transport errors.
		transportErr = nil
	}
	observeUpstreamFailure(UpstreamFailureObservation{
		ChannelID: ra.channel.ID, ChannelKeyID: ra.usedKey.ID,
		Model: ra.metrics.ActualModel, Endpoint: ra.baseURL,
		HTTPStatus: upstreamStatusCode, Headers: upstreamHeaders,
		ResponseBody: upstreamResponseBody, TransportError: transportErr,
		ErrorLevel: decision.Classification.Level.String(),
		ObservedAt: now.UTC(),
	})
	if upstreamStatusCode == http.StatusTooManyRequests && decision.Classification.Level == errorclass.ErrorLevelChannel {
		recordChannelRateLimit(ra.channel.ID, retryAfterDuration(upstreamHeaders, now, defaultRateLimitCooldown))
	}
	// URL 冷却只针对通道级故障（网络/5xx/超时/软错误）。key 级（401/配额）与
	// client 级错误与端点本身无关，误记会让多 URL 渠道被轮换到次优端点。
	endpointFallbackPending := decision.Action == ErrorActionRetryChannel &&
		!ra.isAdaptiveFirstTokenTimeout(fwdErr) && !isFirstTokenBudgetTimeout(fwdErr) && ra.hasNextBaseURL()
	recordURLFailure, healthPenalty := runtimeFailurePenalties(decision, fwdErr, endpointFallbackPending)
	if recordURLFailure {
		recordRuntimeURLFailure(ra.channel.ID, ra.baseURL)
	}
	span.SetRoutingMetadata(safeRuntimeURL(ra.baseURL), attemptDecisionAction(decision, endpointFallbackPending), healthPenalty, ra.selectionReason)
	ra.applyCompactCompatibilityDecision(ctx, decision)
	upstreamSpan.RecordError(fwdErr)
	upstreamSpan.SetStatus(codes.Error, fwdErr.Error())
	upstreamSpan.SetAttributes(attribute.Int("http.response.status_code", upstreamStatusCode))
	span.EndClassified(
		dbmodel.AttemptFailed,
		fwdErr.Error(),
		dbmodel.AttemptErrorLevel(decision.Classification.Level.String()),
		decision.Classification.Reason,
	)
	// Client-scoped upstream errors may still be incompatible with this specific
	// provider, so the runner continues to another candidate. They are not,
	// however, evidence that this channel/key is unhealthy and must not trip the
	// circuit breaker or degrade channel failure statistics.
	if decision.Classification.Level != errorclass.ErrorLevelClient {
		if statsErr := op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:      span.Duration().Milliseconds(),
			RequestFailed: 1,
		}); statsErr != nil {
			log.WithContext(ctx).Warnw("failed to update failed channel attempt statistics",
				"channel_id", ra.channel.ID, "error", statsErr)
		}
		if healthPenalty {
			balancer.RecordFailure(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		} else {
			// 请求内调度超时或仍可进行端点级故障转移时，不提前惩罚
			// channel+key+model；若本次是半开试探则归还名额。
			balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		}
	} else {
		// Client-scoped upstream errors can require a different provider, but do
		// not prove the current channel/key unhealthy. Release any half-open probe
		// slot without recording a circuit-breaker failure.
		balancer.RecordProbeAbort(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
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
			&decision.Classification,
			upstreamStatusCode,
			nil,
			firstTokenDuration,
		)
	}

	// attempt 失败日志（第一阶段可观测性增强 + Phase 4 error_level）
	log.Warnf("attempt %d/%d failed: channel=%s(%d), key=%d, duration=%dms, error_level=%s, error=%v",
		ra.iter.Index()+1, ra.iter.Len(),
		ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
		span.Duration().Milliseconds(), decision.Classification.Level, fwdErr)
	if decision.Action == ErrorActionReturnClient {
		clientErr := &classifiedClientRelayError{
			cause: fwdErr, reason: decision.Classification.Reason, statusCode: decision.ClientStatusCode,
		}
		return false, upstreamHeaders, upstreamResponseBody, newTerminalRelayError(clientErr.StatusCode(), clientErr)
	}
	if decision.Classification.Level == errorclass.ErrorLevelClient {
		return false, upstreamHeaders, upstreamResponseBody, &classifiedClientRelayError{
			cause: fwdErr, reason: decision.Classification.Reason, statusCode: decision.ClientStatusCode,
		}
	}

	return ra.c.Writer.Written(), upstreamHeaders, upstreamResponseBody, fmt.Errorf("channel %s failed: %w", ra.channel.Name, fwdErr)
}

func attemptDecisionAction(decision ErrorDecision, endpointFallbackPending bool) string {
	if endpointFallbackPending {
		return "retry_base_url"
	}
	switch decision.Action {
	case ErrorActionRetryKey:
		return "retry_key"
	case ErrorActionRetryChannel:
		return "retry_channel"
	case ErrorActionReturnClient:
		return "return_client"
	default:
		return "stop"
	}
}

func isRequestContextCanceled(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return false
	}
	// A server-enforced non-streaming deadline is an upstream timeout, not a
	// client cancellation. It must be logged as a failed attempt and surfaced
	// as 504 by the request runner.
	if isNonStreamRequestTimeout(ctx) {
		return false
	}
	return errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (ra *relayAttempt) canRetryNextKey(err error, upstreamHeaders http.Header, upstreamResponseBody []byte) bool {
	if err == nil || ra.c.Writer.Written() || ra.keyIndex+1 >= len(ra.keyOptions) || isChannelRequestLimitError(err) {
		return false
	}

	return ra.decideError(ra.usedKey.StatusCode, upstreamHeaders, upstreamResponseBody, err).Action == ErrorActionRetryKey
}

func (ra *relayAttempt) canRetryNextBaseURL(err error, upstreamHeaders http.Header, upstreamResponseBody []byte) bool {
	if err == nil || ra.c.Writer.Written() || !ra.hasNextBaseURL() ||
		isChannelRequestLimitError(err) || ra.isAdaptiveFirstTokenTimeout(err) || isFirstTokenBudgetTimeout(err) {
		return false
	}
	return ra.decideError(ra.usedKey.StatusCode, upstreamHeaders, upstreamResponseBody, err).Action == ErrorActionRetryChannel
}

func (ra *relayAttempt) hasNextBaseURL() bool {
	for i := ra.baseURLIndex + 1; i < len(ra.baseURLOptions); i++ {
		if ra.baseURLOptions[i] != "" && ra.baseURLOptions[i] != ra.baseURL &&
			ra.requestCandidateAllowed(ra.channel, ra.usedKey, ra.internalRequest.Model, ra.baseURLOptions[i]) {
			return true
		}
	}
	return false
}

func (ra *relayAttempt) switchToNextBaseURL() bool {
	for ra.baseURLIndex+1 < len(ra.baseURLOptions) {
		ra.baseURLIndex++
		nextBaseURL := ra.baseURLOptions[ra.baseURLIndex]
		if nextBaseURL == "" || nextBaseURL == ra.baseURL {
			continue
		}
		if !ra.requestCandidateAllowed(ra.channel, ra.usedKey, ra.internalRequest.Model, nextBaseURL) {
			continue
		}
		outAdapter, err := newChannelOutbound(ra.channel, ra.internalRequest, nextBaseURL, ra.usedKey)
		if err != nil {
			continue
		}
		ra.baseURL = nextBaseURL
		ra.outAdapter = outAdapter
		ra.metrics.ParamOverride = ""
		ra.metrics.OutboundRequestSummary = nil
		ra.metrics.OutboundRequestArtifact = nil
		ra.responseHeaderDuration = 0
		if !ra.selectRequestCandidate(ra) {
			continue
		}
		ra.selectionReason = "base_url_failover"
		return true
	}
	return false
}

func (ra *relayAttempt) switchToNextKey() bool {
	for ra.keyIndex+1 < len(ra.keyOptions) {
		ra.keyIndex++
		nextKey := ra.keyOptions[ra.keyIndex]
		if nextKey.ChannelKey == "" {
			continue
		}
		if !ra.requestCandidateAllowed(ra.channel, nextKey, ra.internalRequest.Model, ra.baseURL) {
			continue
		}
		nextKeyRemark := cleanKeyRemark(nextKey.Remark)
		if ra.iter != nil && ra.iter.SkipCircuitBreak(ra.channel.ID, nextKey.ID, ra.channel.Name, nextKeyRemark) {
			continue
		}
		outAdapter, err := newChannelOutbound(ra.channel, ra.internalRequest, ra.baseURL, nextKey)
		if err != nil {
			// 同 buildRealAttempt：刚被授予的半开试探名额必须归还。
			balancer.RecordProbeAbort(ra.channel.ID, nextKey.ID, ra.internalRequest.Model)
			ra.iter.Skip(ra.channel.ID, nextKey.ID, ra.channel.Name, err.Error())
			continue
		}
		ra.usedKey = nextKey
		ra.keyRemark = nextKeyRemark
		ra.outAdapter = outAdapter
		ra.metrics.ParamOverride = ""
		ra.metrics.OutboundRequestSummary = nil
		ra.metrics.OutboundRequestArtifact = nil
		if !ra.selectRequestCandidate(ra) {
			continue
		}
		ra.selectionReason = "key_failover"
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

// forward 转发请求到上游服务
func (ra *relayAttempt) forward() (int, http.Header, []byte, error) {
	ctx := ra.c.Request.Context()
	if ra.internalRequest.RawRequest == nil {
		return 0, nil, nil, fmt.Errorf("missing raw request")
	}

	httpClient, err := helper.ChannelHttpClient(ra.channel)
	if err != nil {
		log.Warnf("failed to get http client: %v", err)
		return 0, nil, nil, err
	}

	if isCompactOpenAIChannel(ra.channel.Type, ra.internalRequest) {
		return ra.forwardCompact(ctx, httpClient)
	}

	return ra.forwardWithAdapter(ctx, httpClient, ra.outAdapter, requestForOutboundPipeline(ra.channel.Type, ra.internalRequest))
}

// forwardWithAdapter 用指定出站适配器执行一次完整的 pipeline 转发。
func (ra *relayAttempt) forwardWithAdapter(
	ctx context.Context,
	httpClient *http.Client,
	outAdapter transformer.Outbound,
	outboundRequest *llm.Request,
) (int, http.Header, []byte, error) {
	// 更新活跃请求状态为等待上游（第一阶段可观测性增强）
	UpdateState(ra.trackingID, StateWaitingUpstream)
	requestStartedAt := time.Now()
	ra.responseHeaderDuration = 0
	recordResponseHeaders := func() {
		if ra.responseHeaderDuration == 0 {
			ra.responseHeaderDuration = time.Since(requestStartedAt)
		}
	}

	// 首字超时覆盖"发出上游请求 → 收到首个 token"全过程，包含 pipeline.Process 内部阻塞等待响应头的阶段。
	// 出站 HTTP client 没有响应头超时，仅靠 SSE 建立后的计时器无法打断仍卡在 client.Do 等响应头的请求。
	// 流式按首字超时守卫；非流式按 per-attempt 响应头守卫（存在其他候选时），
	// 否则一个挂死渠道会吃光整个 non_stream_timeout_seconds 预算，永远轮不到可用渠道。
	fwdCtx, stopRoutingChangeGuard, releaseRoutingChangeGuard := newRoutingChangeGuard(ctx, ra.routingSnapshot.Changed)
	defer releaseRoutingChangeGuard()
	stopFirstTokenGuard := stopRoutingChangeGuard
	wantStream := ra.internalRequest.Stream != nil && *ra.internalRequest.Stream
	firstTokenTimeout, shadowFirstTokenTimeout := ra.firstTokenTimeoutPolicies()
	if wantStream {
		if remaining := ra.remainingStreamFirstEventBudget(); remaining > 0 {
			if firstTokenTimeout.Duration <= 0 {
				firstTokenTimeout = firstTokenTimeoutConfig{Duration: remaining, Source: firstTokenTimeoutBudget}
			} else if firstTokenTimeout.Duration > remaining {
				firstTokenTimeout.Duration = remaining
				firstTokenTimeout.Source = firstTokenTimeoutBudget
			}
		} else if ra.streamFirstEventBudget > 0 {
			return 0, nil, nil, errStreamFirstEventBudget
		}
	}
	attemptTimeout := firstTokenTimeoutConfig{}
	if wantStream {
		if firstTokenTimeout.Duration > 0 {
			var timeoutStop, release func()
			fwdCtx, timeoutStop, release = newFirstTokenGuard(fwdCtx, firstTokenTimeout.Duration)
			defer release()
			previousStop := stopFirstTokenGuard
			stopFirstTokenGuard = func() {
				timeoutStop()
				previousStop()
			}
		}
		if shadowFirstTokenTimeout.Duration > 0 {
			shadowStop, shadowRelease := newFirstTokenShadowObserver(shadowFirstTokenTimeout.Duration, func() {
				ra.recordShadowFirstTokenTimeout(shadowFirstTokenTimeout)
			})
			previousStop := stopFirstTokenGuard
			stopFirstTokenGuard = func() {
				previousStop()
				shadowStop()
			}
			defer shadowRelease()
		}
	} else {
		attemptTimeout = ra.nonStreamAttemptTimeout()
		if attemptTimeout.Duration > 0 {
			var timeoutStop, release func()
			fwdCtx, timeoutStop, release = newFirstTokenGuard(fwdCtx, attemptTimeout.Duration)
			defer release()
			previousStop := stopFirstTokenGuard
			stopFirstTokenGuard = func() {
				timeoutStop()
				previousStop()
			}
		}
	}

	relayMiddleware := &relayPipelineMiddleware{attempt: ra}
	responseLimit := conf.Current().Relay.MaxNonStreamResponseBytes
	var limitedHTTPClient *http.Client
	ra.streamActivity = nil
	if wantStream {
		activity, touchActivity := newStreamActivitySignal()
		ra.streamActivity = activity
		limitedHTTPClient = httpClientWithResponseLimit(httpClient, responseLimit, false, touchActivity, recordResponseHeaders)
	} else {
		// 非流式在响应头到达时停表：LLM 上游的生成时间几乎全部消耗在
		// 响应头之前，头到达后读 body 不再受 per-attempt 守卫约束
		//（仍受字节上限与全局预算约束），避免误杀合法的大响应传输。
		limitedHTTPClient = httpClientWithResponseLimit(httpClient, responseLimit, true, nil, func() {
			recordResponseHeaders()
			stopFirstTokenGuard()
		})
	}

	result, err := pipeline.NewFactory(httpclient.NewHttpClientWithClient(limitedHTTPClient)).
		Pipeline(
			&parsedRequestInbound{Inbound: ra.inAdapter, request: outboundRequest},
			outAdapter,
			pipeline.WithMiddlewares(stream.EnsureUsage(), relayMiddleware),
			pipeline.WithEmptyResponseDetection(),
		).
		Process(fwdCtx, ra.internalRequest.RawRequest)
	if err != nil {
		if errors.Is(context.Cause(fwdCtx), errRoutingConfigChanged) {
			return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamHeaders, relayMiddleware.upstreamResponseBody, errRoutingConfigChanged
		}
		// 等待响应头阶段触发首字/attempt 超时：此时尚未写客户端，返回明确错误以便切换下一通道。
		if errors.Is(context.Cause(fwdCtx), errFirstTokenTimeout) {
			if wantStream {
				ra.recordFirstTokenTimeout(firstTokenTimeout)
				return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamHeaders, relayMiddleware.upstreamResponseBody, firstTokenTimeout.Error(firstTokenTimeoutPhaseWaitingHeaders)
			}
			return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamHeaders, relayMiddleware.upstreamResponseBody, attemptTimeout.Error(firstTokenTimeoutPhaseWaitingHeaders)
		}
		return relayMiddleware.upstreamStatusCode, relayMiddleware.upstreamHeaders, relayMiddleware.upstreamResponseBody, err
	}
	if result == nil {
		return 0, nil, nil, fmt.Errorf("empty pipeline result")
	}
	if result.Stream {
		if err := ra.writeStream(fwdCtx, stopFirstTokenGuard, firstTokenTimeout, result.EventStream); err != nil {
			var streamErr *streamSoftError
			if errors.As(err, &streamErr) {
				return http.StatusOK, relayMiddleware.upstreamHeaders, streamErr.Body(), err
			}
			return http.StatusOK, relayMiddleware.upstreamHeaders, nil, err
		}
		return http.StatusOK, relayMiddleware.upstreamHeaders, nil, nil
	}
	if result.Response == nil {
		return 0, nil, nil, fmt.Errorf("empty pipeline response")
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
		return statusCode, result.Response.Headers.Clone(), body, fmt.Errorf("soft error detected: upstream returned 200 but content indicates error")
	}

	ra.metrics.InternalResponse = body
	ra.c.Data(statusCode, contentType, body)
	return statusCode, result.Response.Headers.Clone(), body, nil
}
