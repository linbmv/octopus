package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
	"github.com/tidwall/sjson"
)

// Forward 按客户端协议承载一个请求的完整转发过程: 解析请求, 定位分组, 循环选目标请求上游, 直至提交响应或请求结束。
func Forward(format llm.APIFormat) gin.HandlerFunc {
	var inbound transformer.Inbound
	switch format {
	case llm.APIFormatOpenAIResponse:
		inbound = responses.NewInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		inbound = anthropic.NewInboundTransformer()
	default:
		inbound = openai.NewInboundTransformer()
	}

	return func(c *gin.Context) {
		// 完整读取客户端请求, 正文先登记到请求状态, 后续每轮直接改写为当前目标请求。
		raw, err := httpclient.ReadHTTPRequest(c.Request)
		if err != nil {
			rejectRequest(c, inbound, err)
			return
		}

		// 此处只读取选组和分流所需字段; 完整协议校验由同协议上游或跨协议 pipeline 完成。
		var metadata struct {
			Model     string `json:"model"`  // 客户端请求的分组名称。
			Streaming bool   `json:"stream"` // 客户端是否请求流式响应。
		}
		if err := json.Unmarshal(raw.Body, &metadata); err != nil {
			rejectRequest(c, inbound, err)
			return
		}

		// API Key 限定了模型范围时只放行范围内的模型, 为空表示不限制。
		if allowed := c.GetString("supported_models"); allowed != "" && !slices.Contains(strings.Split(allowed, ","), metadata.Model) {
			rejectRequest(c, inbound, errors.New("model not supported by this api key"))
			return
		}

		// 客户端请求的模型名称即分组名称; 分组不存在说明模型名错误, 等待也不会出现该分组。
		if _, err := op.GroupGetEnabledByName(metadata.Model); err != nil {
			rejectRequest(c, inbound, errors.New("model not found"))
			return
		}

		// 登记进程内请求状态, 返回的记录是后续全部状态写入和前端可视化推送的入口。
		request := newRequestState(metadata.Model, string(raw.Body), c.GetInt("api_key_id"))
		ctx := c.Request.Context()
		type failedMember struct {
			groupID int
			itemID  int
		}
		failedKey := failedMember{}            // 当前累计连续失败次数的叶子成员。
		failures := 0                          // 该叶子成员包含首次请求的连续失败次数。
		skippedItems := make(map[int]struct{}) // 当前请求内暂时跳过的熔断/慢恢复成员，不写入全局路由状态。

		for {
			if ctx.Err() != nil {
				request.markCanceled(ctx.Err(), "", nil)
				return
			}

			// 分组配置和成员随时可改, 故每轮重新读取; 分组被删除时等待它重新出现。
			rootGroup, err := op.GroupGetByName(metadata.Model)
			if err != nil || !rootGroup.Enabled {
				if !request.wait(ctx, model.DefaultGroupRelayConfig().MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 手动模式取人工指定的成员, 故障转移模式按优先级选择未禁用且不在冷却中的成员。
			// 没有目标时等待重新选择, 期间人工切换渠道, 补齐成员或成员冷却到期即可让请求继续。
			target, err := pickGroupLeafSkipping(ctx, rootGroup, skippedItems)
			if err != nil && ctx.Err() != nil {
				request.markCanceled(ctx.Err(), "", nil)
				return
			}
			if target == nil {
				clear(skippedItems)
				if !request.wait(ctx, rootGroup.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			group := target.group
			item := target.item

			channelModel := item.ChannelModel
			if channelModel == nil {
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}

			// 成员指向的渠道已被删除时同样等待, 该成员可能很快被改回可用渠道。
			channel, err := op.ChannelGet(channelModel.ChannelID)
			if err != nil {
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			member := failedMember{groupID: group.ID, itemID: item.ID}
			attempt := 0
			if failedKey == member {
				attempt = failures
			}
			channel, channelKeyID, circuitPermit, err := selectChannelEndpointForModel(channel, attempt, channelModel.Name)
			if err != nil {
				request.finishRound(err.Error())
				var circuitErr *circuitUnavailableError
				if errors.As(err, &circuitErr) {
					// A breaker decision is already cross-request state. Keep this
					// skip local so another group member can be tried without
					// conflating the two state machines.
					skippedItems[item.ID] = struct{}{}
					continue
				}
				clear(skippedItems)
				if group.Mode == model.GroupModeManual {
					if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
						return
					}
					continue
				}
				// A channel with no available key is a real candidate failure. It
				// follows the same route cooldown path as an upstream auth failure.
				if failedKey == member {
					failures++
				} else {
					failedKey, failures = member, 1
				}
				if recordRouteFailurePath(target.path, failures) {
					continue
				}
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			slowKey := newSlowRecoveryKey(channel.ID, channelKeyID, channelModel.Name, channel.BaseURL)
			slowAllowed, slowLease, slowRemaining := globalSlowRecovery.acquire(slowKey, slowRecoveryBudget(group, metadata.Streaming))
			if !slowAllowed {
				// Slow recovery is a separate gate. If this candidate was
				// admitted as a half-open breaker probe, the request did not
				// reach the upstream and must return that probe slot.
				abortCircuitProbe(circuitPermit)
				request.finishRound(slowRecoveryBackoffMessage(slowRemaining))
				skippedItems[item.ID] = struct{}{}
				continue
			}
			abortUnstartedAttempt := func() {
				abortCircuitProbe(circuitPermit)
				globalSlowRecovery.release(slowKey, slowLease)
			}

			// 将分组成员配置的真实模型写入本轮上游请求。
			raw.Body, err = sjson.SetBytes(raw.Body, "model", channelModel.Name)
			if err != nil {
				abortUnstartedAttempt()
				request.markFailed(err, "", nil)
				rejectRequest(c, inbound, err)
				return
			}
			// OpenAI Chat 流式响应需显式要求上游在末尾附带用量。
			if metadata.Streaming && format == llm.APIFormatOpenAIChatCompletion {
				raw.Body, err = sjson.SetBytes(raw.Body, "stream_options.include_usage", true)
				if err != nil {
					abortUnstartedAttempt()
					request.markFailed(err, "", nil)
					rejectRequest(c, inbound, err)
					return
				}
			}

			// 为本轮上游调用建立独立取消入口并登记当前目标; 取消原因用于区分人工中止与响应超时。
			roundCtx, cancelRoundCause := context.WithCancelCause(ctx)
			// 人工中止和本轮完成都使用普通 canceled 原因, 超时回调则写入具体的超时错误。
			cancelRound := func() {
				cancelRoundCause(context.Canceled)
			}
			request.startRound(cancelRound, channel.Name, channelModel.Name)

			// 按渠道协议构造出站转换器并确定是否可以直接透传。
			roundStartedAt := time.Now() // 本轮上游调用的开始时间, 用于统计首个有效响应耗时。
			outbound, passthrough, err := buildOutbound(channel, format)
			upstreamStarted := err == nil

			// 请求上游并等待首个有效响应: 非流式等待完整响应, 流式等待首个事件。
			// 同协议渠道原样直通, 跨协议渠道经转换后请求; 此时尚未写给客户端, 失败仍可换目标重试。
			var result *upstreamResponse
			if err == nil {
				timeoutSeconds := group.RelayConfig.MemberNonStreamResponseTimeoutSeconds // 非流式等待完整响应, 流式分支改为首事件超时。
				timeoutErr := errUpstreamNonStreamResponseTimeout                         // 具体错误用于区分超时与人工中止。
				if metadata.Streaming {
					timeoutSeconds = group.RelayConfig.MemberStreamFirstEventTimeoutSeconds
					timeoutErr = errUpstreamStreamFirstEventTimeout
				}
				// 计时器取消本轮上下文, 让正在等待 HTTP 响应或首个流事件的调用及时返回。
				timeoutTimer := time.AfterFunc(time.Duration(timeoutSeconds)*time.Second, func() {
					cancelRoundCause(timeoutErr)
				})
				// 客户端与渠道协议一致时直接透传, 其余组合通过 pipeline 转换。
				if passthrough {
					result, err = sendPassthrough(roundCtx, format, raw, channel, outbound, metadata.Streaming)
				} else {
					result, err = sendConverted(roundCtx, format, raw, channel, outbound, metadata.Streaming)
				}
				// 上游调用返回即结束首响应等待; Stop 失败说明已到期, 主动取消可避免等待异步回调完成。
				if !timeoutTimer.Stop() {
					cancelRoundCause(timeoutErr)
				}
				if context.Cause(roundCtx) == timeoutErr {
					err = timeoutErr
					// 超时与响应返回同时发生时舍弃尚未提交的流结果, 避免把超时误记为成功。
					if result != nil && result.events != nil {
						result.events.Close()
					}
				}
			}

			if err != nil {
				// 记录本轮上游调用已经结束及其失败原因。
				request.finishRound(err.Error())
				// 父上下文结束说明客户端已经取消, 归还探测占用并以取消终态结束请求。
				if ctx.Err() != nil {
					abortUnstartedAttempt()
					releaseRouteProbePath(target.path)
					request.markCanceled(ctx.Err(), "", nil)
					return
				}
				// 仅人工中止本轮时不计失败也不等待; 响应超时属于真实失败并消耗尝试次数。
				if context.Cause(roundCtx) == context.Canceled {
					abortUnstartedAttempt()
					releaseRouteProbePath(target.path)
					continue
				}
				if !upstreamStarted {
					abortUnstartedAttempt()
					clear(skippedItems)
					key := failedMember{groupID: group.ID, itemID: item.ID}
					if failedKey == key {
						failures++
					} else {
						failedKey, failures = key, 1
					}
					if recordRouteFailurePath(target.path, failures) {
						continue
					}
					if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
						return
					}
					continue
				}
				cancelRound()
				// 本轮真实失败只计入当前渠道和成员, 客户端取消与人工中止不计为渠道故障。
				metrics := model.StatsMetrics{WaitTime: time.Since(roundStartedAt).Milliseconds(), RequestFailed: 1}
				_ = op.ChannelStatsUpdate(channel.ID, metrics)
				_ = op.ChannelModelStatsUpdate(channelModel.ID, metrics)
				// 上游限流需要落到具体凭据上, 否则同一个 429 的 Key 会被后续请求反复选中。
				status, retryAfter := upstreamFailureStatus(err, time.Now())
				op.ChannelKeyHealthUpdate(op.ChannelKeyHealthReport{
					ChannelID:  channel.ID,
					KeyID:      channelKeyID,
					StatusCode: status,
					RetryAfter: retryAfter,
				})

				// 请求本身非法时换渠道也是同样结果, 继续遍历只会把每个成员都判为故障。
				// 直接把上游错误返回给客户端, 让调用方修正请求。
				// 请求本身不合法时继续换渠道只会把同一个错误放大到每个成员, 因此立即终止。
				// 该分支尚未提交响应, 可以按客户端协议写回上游的错误体。
				class := classifyUpstreamFailure(err)
				if class.Level == errorclass.ErrorLevelClient {
					abortUnstartedAttempt()
					releaseRouteProbePath(target.path)
					body := respondUpstreamClientError(c, inbound, err)
					request.markFailed(fmt.Errorf("%s (%s): %w", class.Reason, class.Level, err), body, nil)
					return
				}
				recordCircuitFailure(channel.ID, channelKeyID, channelModel.Name, circuitPermit)
				if isSlowRecoveryTimeout(err) {
					globalSlowRecovery.recordTimeout(slowKey, slowLease)
				} else {
					globalSlowRecovery.recordSuccess(slowKey, slowLease)
				}
				clear(skippedItems)

				// 成员改变时重新开始累计该成员在本请求内的连续失败次数。
				key := failedMember{groupID: group.ID, itemID: item.ID}
				if failedKey == key {
					failures++
				} else {
					failedKey = key
					failures = 1
				}
				// 达到总尝试次数时成员进入冷却并立即重新选路, 否则等待后重试。
				if recordRouteFailurePath(target.path, failures) {
					continue
				}
				if !request.wait(ctx, group.RelayConfig.MemberRetryIntervalSeconds) {
					return
				}
				continue
			}
			// 记录本轮已经取得可提交的上游响应。
			request.finishRound("")
			roundWaitTime := time.Since(roundStartedAt).Milliseconds() // 流式响应只统计等待首帧的时间。
			recordCircuitSuccess(channel.ID, channelKeyID, channelModel.Name, circuitPermit)
			globalSlowRecovery.recordSuccess(slowKey, slowLease)
			clear(skippedItems)
			// 上游成功后解除该成员的冷却与探测占用, 并按路由配置开始亲和。
			recordRouteSuccessPath(target.path)
			// 成功同样要写回凭据健康态, 使之前被限流的 Key 及时解除冷却。
			op.ChannelKeyHealthUpdate(op.ChannelKeyHealthReport{
				ChannelID:  channel.ID,
				KeyID:      channelKeyID,
				StatusCode: http.StatusOK,
			})
			// 同协议透传时原样返回上游响应头; 跨协议响应没有需要透传的响应头。
			for key, values := range result.header {
				c.Writer.Header()[key] = values
			}

			// 非流式响应已经完整取得, 提交后一次写给客户端。
			if !metadata.Streaming {
				cancelRound()
				if c.Writer.Header().Get("Content-Type") == "" {
					c.Header("Content-Type", "application/json")
				}
				// 非流式响应已有完整用量, 本轮渠道和成员统计可在提交前一次完成。
				metrics := usageMetrics(channelModel.Name, result.usage)
				metrics.WaitTime = roundWaitTime
				metrics.RequestSuccess = 1
				_ = op.ChannelStatsUpdate(channel.ID, metrics)
				_ = op.ChannelModelStatsUpdate(channelModel.ID, metrics)
				request.markCommitted()
				n, err := c.Writer.Write(result.body)
				if err == nil && n != len(result.body) {
					err = io.ErrShortWrite
				}
				if err != nil {
					if ctx.Err() != nil {
						request.markCanceled(ctx.Err(), string(result.body), result.usage)
					} else {
						request.markFailed(err, string(result.body), result.usage)
					}
					return
				}
				request.markSucceeded(string(result.body), result.usage)
				return
			}

			// 首帧提交后仍需逐个事件判断协议终态: 上游发出结束事件后未必立即关闭响应体, 继续读取会一直阻塞到
			// 客户端断开, 从而把已完整交付的响应误判为 context canceled。
			if c.Writer.Header().Get("Content-Type") == "" {
				c.Header("Content-Type", "text/event-stream")
			}
			var encoded bytes.Buffer
			var chunks []*httpclient.StreamEvent
			event := result.first
			last := result.last // 已转发的最后一个事件是否已按客户端协议结束整个响应流。
			committed := false
			for {
				if event != nil {
					chunks = append(chunks, event)
					encoded.Reset()
					if encodeErr := sse.Encode(&encoded, sse.Event{Id: event.LastEventID, Event: event.Type, Data: event.Data}); encodeErr != nil {
						err = encodeErr
						break
					}
					if !committed {
						request.markCommitted()
						committed = true
					}
					n, writeErr := c.Writer.Write(encoded.Bytes())
					if writeErr == nil && n != encoded.Len() {
						writeErr = io.ErrShortWrite
					}
					if writeErr != nil {
						err = writeErr
						break
					}
					c.Writer.Flush()
				}
				if last {
					break
				}
				if !result.events.Next() {
					err = result.events.Err()
					break
				}
				event = result.events.Current()
				// 已提交的响应不能再换目标重试, 结束事件自身携带的失败原样转发给客户端, 并在转发后作为本请求终态。
				last, err = inspectStreamEvent(format, event)
			}
			result.events.Close()
			cancelRound()
			// 使用客户端协议转换器聚合已转发事件, 统一取得最终响应正文和用量。
			responseBody, meta, aggregateErr := inbound.AggregateStreamChunks(context.WithoutCancel(ctx), chunks)
			if aggregateErr == nil {
				result.usage = meta.Usage
			}
			// 流式响应结束并聚合出用量后, 按最终结果完成本轮渠道和成员统计。
			metrics := usageMetrics(channelModel.Name, result.usage)
			metrics.WaitTime = roundWaitTime
			if err == nil {
				metrics.RequestSuccess = 1
			} else {
				metrics.RequestFailed = 1
			}
			_ = op.ChannelStatsUpdate(channel.ID, metrics)
			_ = op.ChannelModelStatsUpdate(channelModel.ID, metrics)
			if err != nil {
				if ctx.Err() != nil {
					request.markCanceled(ctx.Err(), string(responseBody), result.usage)
				} else {
					request.markFailed(err, string(responseBody), result.usage)
				}
				return
			}
			request.markSucceeded(string(responseBody), result.usage)
			return
		}
	}
}

// respondUpstreamClientError 以客户端协议返回上游判定为请求级的失败, 并回传写给客户端的响应体。
// 该分支只在提交响应之前触发, 因此可以安全写入完整错误体; 上游状态码与错误信息一并保留,
// 否则调用方只会看到一个空响应, 无法得知请求哪里不合法。
func respondUpstreamClientError(c *gin.Context, inbound transformer.Inbound, err error) string {
	status := http.StatusBadRequest
	message := err.Error()
	var failure *httpclient.Error
	if errors.As(err, &failure) {
		if failure.StatusCode > 0 {
			status = failure.StatusCode
		}
		if len(failure.Body) > 0 {
			message = string(failure.Body)
		}
	}
	response := inbound.TransformError(c.Request.Context(), &llm.ResponseError{
		StatusCode: status,
		Detail:     llm.ErrorDetail{Message: message, Type: "invalid_request_error"},
	})
	c.Data(response.StatusCode, "application/json", response.Body)
	c.Abort()
	return string(response.Body)
}

// rejectRequest 以客户端协议的错误格式返回请求级失败, 用于尚未登记状态因而无需定稿的请求。
func rejectRequest(c *gin.Context, inbound transformer.Inbound, err error) {
	response := inbound.TransformError(c.Request.Context(), &llm.ResponseError{
		StatusCode: http.StatusBadRequest,
		Detail:     llm.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"},
	})
	c.Data(response.StatusCode, "application/json", response.Body)
	c.Abort()
}
