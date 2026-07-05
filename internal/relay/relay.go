package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/pipeline/stream"
	"github.com/looplj/axonhub/llm/transformer"
)

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
