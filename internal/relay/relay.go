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
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/jsonpatch"
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

	group, err := op.GroupGetEnabledMap(internalRequest.Model, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "model not found")
		return nil, err
	}

	apiKeyID := c.GetInt("api_key_id")
	iter := balancer.NewIterator(group, apiKeyID, internalRequest.Model)
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
		iter:  iter,
		group: group,
	}, nil
}

func (r *relayRun) run() {
	ctx := r.c.Request.Context()
	var lastErr error

	for r.iter.Next() {
		select {
		case <-ctx.Done():
			log.Infof("request context canceled, stopping retry")
			r.metrics.Save(ctx, false, context.Canceled, r.iter.Attempts())
			return
		default:
		}

		attempt, err := r.prepareAttempt()
		if err != nil {
			lastErr = err
			continue
		}
		if attempt == nil {
			continue
		}

		written, err := attempt.run()
		if err == nil {
			r.metrics.Save(ctx, true, nil, r.iter.Attempts())
			return
		}
		if written {
			r.metrics.Save(ctx, false, err, r.iter.Attempts())
			return
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("all channels failed")
	}
	r.metrics.Save(ctx, false, lastErr, r.iter.Attempts())
	resp.Error(r.c, http.StatusBadGateway, lastErr.Error())
}

func (r *relayRun) prepareAttempt() (*relayAttempt, error) {
	item := r.iter.Item()
	return r.resolveGroupItem(item, r.iter.IsSticky(), r.iter.StickyKeyID())
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

	usedKey := dbmodel.ChannelKey{}
	if stickyKeyID > 0 {
		usedKey = channel.GetChannelKeyByID(stickyKeyID)
	}
	if usedKey.ChannelKey == "" {
		usedKey = channel.GetChannelKey()
	}
	if usedKey.ChannelKey == "" {
		r.iter.Skip(channel.ID, 0, channel.Name, "no available key")
		return nil, nil
	}

	keyRemark := cleanKeyRemark(usedKey.Remark)
	if r.iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name, keyRemark) {
		return nil, nil
	}

	outAdapter, err := newOutbound(channel.Type, r.internalRequest, channel.GetBaseUrl(), usedKey.ChannelKey)
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
		usedKey:    usedKey,
		keyRemark:  keyRemark,
	}, nil
}

// run 统一管理一次通道尝试的完整生命周期。
func (ra *relayAttempt) run() (bool, error) {
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

	upstreamStatusCode, fwdErr := ra.forward()
	if fwdErr == nil && upstreamStatusCode == 0 {
		upstreamStatusCode = http.StatusOK
	}
	ra.usedKey.StatusCode = upstreamStatusCode
	ra.usedKey.LastUseTimeStamp = time.Now().Unix()

	if fwdErr == nil {
		ra.usedKey.TotalCost += ra.metrics.Stats.InputCost + ra.metrics.Stats.OutputCost
		op.ChannelKeyUpdate(ra.usedKey)

		span.End(dbmodel.AttemptSuccess, "")
		op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:       span.Duration().Milliseconds(),
			RequestSuccess: 1,
		})
		balancer.RecordSuccess(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)

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
			if d, ok := span.FirstTokenDuration(); ok {
				firstTokenMs = d.Milliseconds()
			}
		}
		log.Infof("attempt %d/%d success: channel=%s(%d), key=%d, duration=%dms, first_token=%dms, sticky_updated=%t",
			ra.iter.Index()+1, ra.iter.Len(),
			ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
			span.Duration().Milliseconds(), firstTokenMs, shouldSticky)

		return false, nil
	}

	op.ChannelKeyUpdate(ra.usedKey)
	span.End(dbmodel.AttemptFailed, fwdErr.Error())
	op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
		WaitTime:      span.Duration().Milliseconds(),
		RequestFailed: 1,
	})
	balancer.RecordFailure(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)

	// attempt 失败日志（第一阶段可观测性增强）
	log.Warnf("attempt %d/%d failed: channel=%s(%d), key=%d, duration=%dms, error=%v",
		ra.iter.Index()+1, ra.iter.Len(),
		ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
		span.Duration().Milliseconds(), fwdErr)

	return ra.c.Writer.Written(), fmt.Errorf("channel %s failed: %v", ra.channel.Name, fwdErr)
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
func (ra *relayAttempt) forward() (int, error) {
	ctx := ra.c.Request.Context()
	if ra.internalRequest.RawRequest == nil {
		return 0, fmt.Errorf("missing raw request")
	}

	httpClient, err := helper.ChannelHttpClient(ra.channel)
	if err != nil {
		log.Warnf("failed to get http client: %v", err)
		return 0, err
	}

	// 先按渠道原生类型尝试（Response 渠道 → /v1/responses/compact，Chat 渠道 → /v1/chat/completions）。
	// Chat 渠道承接 Compact 时，请求会降级为 Chat，但客户端仍期望 Compact 响应。
	statusCode, fwdErr := ra.forwardWithAdapter(ctx, httpClient, ra.outAdapter, requestForOutboundPipeline(ra.channel.Type, ra.internalRequest), needsChatToCompactResponse(ra.channel.Type, ra.internalRequest))
	if fwdErr == nil {
		return statusCode, nil
	}

	// Response 渠道的 Compact 端点级降级：上游不支持 /v1/responses/compact 时，
	// 先在同一渠道、同一 key 上改用普通 /v1/responses 重试；若 /responses 也不支持，
	// 再降到 /v1/chat/completions。这样兼容 Responses-only 中转站，避免直接打到不支持的 Chat 端点。
	// 严格收口：仅 Compact 请求 + Response 类型渠道 + 端点不支持错误 + 客户端尚未收到任何数据。
	if ra.canDowngradeCompactEndpoint(fwdErr) {
		responsesFallbackRequest := compactResponsesFallbackRequest(ra.internalRequest)
		log.Infof("compact endpoint downgrade: channel=%s(%d), retrying via /v1/responses", ra.channel.Name, ra.channel.ID)
		statusCode, responsesErr := ra.forwardWithAdapter(ctx, httpClient, ra.outAdapter, responsesFallbackRequest, true)
		if responsesErr == nil {
			return statusCode, nil
		}
		if !ra.canDowngradeCompactEndpoint(responsesErr) {
			return statusCode, responsesErr
		}

		chatAdapter, buildErr := newOutbound(llm.APIFormatOpenAIChatCompletion, ra.internalRequest, ra.channel.GetBaseUrl(), ra.usedKey.ChannelKey)
		if buildErr != nil {
			log.Warnf("compact endpoint downgrade: build chat outbound failed: %v", buildErr)
			return statusCode, responsesErr
		}
		log.Infof("compact endpoint downgrade: channel=%s(%d), retrying via /v1/chat/completions", ra.channel.Name, ra.channel.ID)
		// 最后尝试使用 Chat 端点，并标记需要 Chat→Compact 响应转换。
		return ra.forwardWithAdapter(ctx, httpClient, chatAdapter, compactChatFallbackRequest(ra.internalRequest), true)
	}

	return statusCode, fwdErr
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

func needsChatToCompactResponse(channelType llm.APIFormat, request *llm.Request) bool {
	return channelType == llm.APIFormatOpenAIChatCompletion && request != nil && request.RequestType == llm.RequestTypeCompact
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
) (int, error) {
	// 更新活跃请求状态为等待上游（第一阶段可观测性增强）
	UpdateState(ra.trackingID, StateWaitingUpstream)

	// 首字超时覆盖"发出上游请求 → 收到首个 token"全过程，包含 pipeline.Process 内部阻塞等待响应头的阶段。
	// 出站 HTTP client 没有响应头超时，仅靠 SSE 建立后的计时器无法打断仍卡在 client.Do 等响应头的请求。
	// 只对客户端流式请求生效：非流式没有"首个 token"语义，整段响应可能合法地长于该阈值。
	fwdCtx := ctx
	stopFirstTokenGuard := func() {}
	wantStream := ra.internalRequest.Stream != nil && *ra.internalRequest.Stream
	if sec := ra.group.FirstTokenTimeOut; sec > 0 && wantStream {
		var release func()
		fwdCtx, stopFirstTokenGuard, release = newFirstTokenGuard(ctx, time.Duration(sec)*time.Second)
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
			return relayMiddleware.upstreamStatusCode, fmt.Errorf("first token timeout (%ds)", ra.group.FirstTokenTimeOut)
		}
		return relayMiddleware.upstreamStatusCode, err
	}
	if result == nil {
		return 0, fmt.Errorf("empty pipeline result")
	}
	if result.Stream {
		if err := ra.writeStream(fwdCtx, stopFirstTokenGuard, result.EventStream); err != nil {
			return http.StatusOK, err
		}
		return http.StatusOK, nil
	}
	if result.Response == nil {
		return 0, fmt.Errorf("empty pipeline response")
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
		return statusCode, fmt.Errorf("soft error detected: upstream returned 200 but content indicates error")
	}

	ra.metrics.InternalResponse = body
	ra.c.Data(statusCode, contentType, body)
	return statusCode, nil
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

// applyRawPassthrough 在 OpenAI Chat 入站转 OpenAI Chat 出站、且渠道开启开关时，
// 用客户端原始 JSON body 替换 outbound transformer 重序列化后的 body，避免字段重排和未知字段丢失，
// 以提升上游 prompt cache 命中率。仅替换顶层 model 为本次 attempt 的真实上游模型，并按需补流式 usage 标记。
// 返回 true 表示本次确实用原始 body 替换了出站请求。
func (ra *relayAttempt) applyRawPassthrough(outboundRequest *httpclient.Request) bool {
	if !ra.channel.RawPassthrough {
		return false
	}
	// 入站与出站都必须是 OpenAI Chat，且为 chat 请求，避免把原始 body 透传给异构协议或 embedding/image 等请求。
	if ra.internalRequest.APIFormat != llm.APIFormatOpenAIChatCompletion || ra.channel.Type != llm.APIFormatOpenAIChatCompletion {
		return false
	}
	if ra.internalRequest.RequestType != "" && ra.internalRequest.RequestType != llm.RequestTypeChat {
		return false
	}
	if ra.internalRequest.RawRequest == nil || len(ra.internalRequest.RawRequest.Body) == 0 {
		return false
	}
	// 出站必须是 JSON body；multipart 等非 JSON 请求不参与透传。
	if !strings.Contains(strings.ToLower(outboundRequest.Headers.Get("Content-Type")+" "+outboundRequest.ContentType), "application/json") {
		return false
	}

	rawBody := ra.internalRequest.RawRequest.Body
	// 安全门槛：raw body 必须是带顶层 string model 的合法 JSON 对象，否则回退常规转换路径，绝不发送错误模型。
	rawModel, ok := jsonpatch.TopLevelModel(rawBody)
	if !ok {
		return false
	}

	// 复制后再 patch，禁止原地修改 RawRequest.Body，避免重试或切换渠道时污染原始请求。
	patched := make([]byte, len(rawBody))
	copy(patched, rawBody)

	if rawModel != ra.internalRequest.Model {
		// 实际上游模型与原始请求不同：必须 patch 成功才能透传，否则会把请求发到错误模型上。
		next, modelPatched := jsonpatch.PatchModel(patched, ra.internalRequest.Model)
		if !modelPatched {
			return false
		}
		patched = next
	}

	// 流式请求补 stream_options.include_usage=true：raw body 替换发生在 stream.EnsureUsage 之后，
	// 不补这个标记会丢失 usage 聚合所需的最终 usage chunk。
	if ra.internalRequest.Stream != nil && *ra.internalRequest.Stream {
		if next, _ := jsonpatch.EnsureStreamIncludeUsage(patched); next != nil {
			patched = next
		}
	}

	outboundRequest.Body = patched
	return true
}

// writeStream 把 pipeline 输出的客户端格式流写回请求方，并保留首 token 超时切换通道的行为。
// stopFirstTokenGuard 在收到首个 token 后调用，停止 forward 阶段建立的首字超时计时器。
func (ra *relayAttempt) writeStream(ctx context.Context, stopFirstTokenGuard func(), clientStream streams.Stream[*httpclient.StreamEvent]) error {
	if clientStream == nil {
		return fmt.Errorf("empty pipeline stream")
	}
	if stopFirstTokenGuard == nil {
		stopFirstTokenGuard = func() {}
	}

	// 更新活跃请求状态为流式传输
	UpdateState(ra.trackingID, StateStreaming)

	// 设置 SSE 响应头
	ra.c.Header("Content-Type", "text/event-stream")
	ra.c.Header("Cache-Control", "no-cache")
	ra.c.Header("Connection", "keep-alive")
	ra.c.Header("X-Accel-Buffering", "no")

	firstToken := true
	responseEvents := make([]*httpclient.StreamEvent, 0, 8)
	type sseReadResult struct {
		event *httpclient.StreamEvent
		err   error
	}
	results := make(chan sseReadResult, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(results)
		defer clientStream.Close()
		defer func() {
			if r := recover(); r != nil {
				log.Warnf("stream reader panic: %v", r)
				select {
				case results <- sseReadResult{err: fmt.Errorf("stream reader panic: %v", r)}:
				case <-done:
				case <-ctx.Done():
				}
			}
		}()
		// Next 可能阻塞等待上游 token；放到协程里让首 token 超时和客户端断开都能及时打断本次通道尝试。
		readerFirst := true
		for clientStream.Next() {
			event := clientStream.Current()
			// 收到首个有效事件立刻停掉首字超时计时器：在事件入队前就赢下与计时器的 CAS 竞态，
			// 消除"事件已到、主循环尚未取走"窗口内计时器误取消 context 导致误切通道/截断流的问题。
			if readerFirst && event != nil && len(event.Data) > 0 {
				stopFirstTokenGuard()
				readerFirst = false
			}
			select {
			case results <- sseReadResult{event: event}:
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
		if err := clientStream.Err(); err != nil {
			select {
			case results <- sseReadResult{err: err}:
			case <-done:
			case <-ctx.Done():
			}
		}
	}()

	firstTokenTimeoutSec := ra.group.FirstTokenTimeOut

	for {
		select {
		case <-ctx.Done():
			// 首字超时在收到首个 token 前触发：返回错误以切换下一通道。
			// 其余取消（客户端断开、或首 token 之后的取消）按正常停止处理，不再切换通道。
			if firstToken && errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
				log.Warnf("first token timeout (%ds), switching channel", firstTokenTimeoutSec)
				_ = clientStream.Close()
				return fmt.Errorf("first token timeout (%ds)", firstTokenTimeoutSec)
			}
			log.Infof("client disconnected, stopping stream")
			_ = clientStream.Close()
			return nil
		case r, ok := <-results:
			if !ok {
				log.Infof("stream end")
				if len(responseEvents) == 0 {
					return nil
				}
				// 客户端请求流式时，pipeline 只负责边转边写，不会自动生成完整响应体。
				// 这里复用同一个 inbound 聚合器把已经写给客户端的事件合成最终 body，日志只落一次最终响应。
				responseBody, meta, err := ra.inAdapter.AggregateStreamChunks(context.WithoutCancel(ctx), responseEvents)
				if err != nil {
					log.Warnf("failed to aggregate stream response for log: %v", err)
					return nil
				}
				ra.metrics.InternalResponse = responseBody
				ra.metrics.RecordUsage(meta.Usage)
				return nil
			}
			if r.err != nil {
				log.Warnf("failed to read event: %v", r.err)
				return fmt.Errorf("failed to read stream event: %w", r.err)
			}

			if r.event == nil || len(r.event.Data) == 0 {
				continue
			}
			// 这里只临时保存 pipeline 已经转换好的客户端格式事件，正常结束后聚合成最终响应体用于日志；不会把分片逐条落库。
			event := r.event
			responseEvents = append(responseEvents, event)
			if firstToken {
				now := time.Now()
				ra.metrics.FirstTokenTime = now
				// 记录首 token 时间到 attempt span（第一阶段可观测性增强）
				if ra.span != nil {
					ra.span.RecordFirstToken(now)
				}
				firstToken = false
				// 首字超时计时器已由 reader 协程在事件入队前停掉，这里只记录首 token 时间。
				// 仍兜底调用一次（幂等）以防 reader 停表路径未覆盖到的边界。
				stopFirstTokenGuard()
			}

			ra.c.SSEvent(event.Type, event.Data)
			ra.c.Writer.Flush()
		}
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
// 2. 在上游失败时保存 HTTP 状态码，供 key 冷却、熔断和后续选路使用；
// 3. 在非流式响应转成 llm.Response 后记录 usage。
// axonhub/llm 只提供了部分函数式 middleware 构造器，错误状态码和 llm 响应 usage 这两个回调没有公开构造器，
// 所以这里保留一个很薄的结构体实现完整接口，而不是在 relay 主流程里重复 pipeline 的执行逻辑。
type relayPipelineMiddleware struct {
	pipeline.DummyMiddleware
	attempt            *relayAttempt
	upstreamStatusCode int
}

func (m *relayPipelineMiddleware) Name() string {
	return "octopus_relay"
}

func (m *relayPipelineMiddleware) OnOutboundRawRequest(ctx context.Context, request *httpclient.Request) (*httpclient.Request, error) {
	if request.Headers == nil {
		request.Headers = make(http.Header)
	}
	m.attempt.applyChannelRequestOptions(request)
	return request, nil
}

func (m *relayPipelineMiddleware) OnOutboundRawError(ctx context.Context, err error) {
	var upstreamErr *httpclient.Error
	if errors.As(err, &upstreamErr) {
		// pipeline 会把上游错误转换成统一错误返回；这里在转换前记录原始 HTTP 状态码，用于渠道 key 的后续调度决策。
		m.upstreamStatusCode = upstreamErr.StatusCode
	}
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

// compactResponsesFallbackRequest 把 Compact 请求降级成可由普通 OpenAI Responses 端点处理的副本。
func compactResponsesFallbackRequest(request *llm.Request) *llm.Request {
	attemptRequest := compactConversationFallbackRequest(request)
	attemptRequest.APIFormat = llm.APIFormatOpenAIResponse
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
// 用于端点降级场景：/v1/responses/compact 不支持时改用 /v1/responses 或 /v1/chat/completions，
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
		// 从第一个 Choice 提取内容
		firstChoice := response.Choices[0]
		output := []llm.Message{}
		if firstChoice.Message != nil {
			output = append(output, *firstChoice.Message)
		}

		// 构造 Compact 响应
		response.Compact = &llm.CompactResponse{
			ID:        response.ID,
			CreatedAt: response.Created,
			Object:    "response.compaction",
			Output:    output,
		}
	}
	if response.Compact == nil {
		response.Compact = &llm.CompactResponse{
			ID:        response.ID,
			CreatedAt: response.Created,
			Object:    "response.compaction",
			Output:    []llm.Message{},
		}
	}
	response.RequestType = llm.RequestTypeCompact
	response.APIFormat = llm.APIFormatOpenAIResponseCompact

	return response, nil
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
