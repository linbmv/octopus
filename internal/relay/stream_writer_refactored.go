package relay

import (
	"context"
	"errors"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
)

// handleContextDone 处理 context 取消的情况（首 token 超时或客户端断开）
func (ra *relayAttempt) handleContextDone(ctx context.Context, firstToken bool, firstTokenTimeout firstTokenTimeoutConfig, closeStream func()) error {
	// 首字超时在收到首个 token 前触发：返回错误以切换下一通道。
	if firstToken && errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
		timeoutErr := firstTokenTimeout.Error(firstTokenTimeoutPhaseStreamFirstEvent)
		log.Warnf("%v, switching channel", timeoutErr)
		ra.recordFirstTokenTimeout(firstTokenTimeout)
		closeStream()
		return timeoutErr
	}
	// 客户端断开不是上游成功，必须向外返回取消错误
	log.Infof("client disconnected, stopping stream")
	closeStream()
	return context.Canceled
}

// handleStreamEnd 处理流结束时的日志聚合和指标记录
func (ra *relayAttempt) handleStreamEnd(ctx context.Context, responseLog *streamLogCollector) error {
	log.Infof("stream end")
	if responseLog.Empty() {
		return nil
	}
	if responseLog.Truncated() {
		ra.metrics.InternalResponse = responseLog.TruncatedBody()
		ra.metrics.RecordUsage(responseLog.Usage())
		return nil
	}
	// 客户端请求流式时，pipeline 只负责边转边写，不会自动生成完整响应体。
	// 这里复用同一个 inbound 聚合器把已经写给客户端的事件合成最终 body，日志只落一次最终响应。
	responseBody, meta, err := ra.inAdapter.AggregateStreamChunks(context.WithoutCancel(ctx), responseLog.Events())
	if err != nil {
		log.Warnf("failed to aggregate stream response for log: %v", err)
		return nil
	}
	ra.metrics.InternalResponse = responseBody
	ra.metrics.RecordUsage(meta.Usage)
	return nil
}

// handleFirstToken 处理首个 token 到达时的逻辑
func (ra *relayAttempt) handleFirstToken(stopFirstTokenGuard func()) {
	now := time.Now()
	ra.metrics.FirstTokenTime = now
	// 记录首 token 时间到 attempt span（第一阶段可观测性增强）
	if ra.span != nil {
		ra.span.RecordFirstToken(now)
	}
	// 首字超时计时器已由 reader 协程在事件入队前停掉，这里只记录首 token 时间。
	// 仍兜底调用一次（幂等）以防 reader 停表路径未覆盖到的边界。
	stopFirstTokenGuard()
}

// writeEventToClient 将事件写入客户端 SSE 流
func (ra *relayAttempt) writeEventToClient(event *httpclient.StreamEvent) {
	ra.c.SSEvent(event.Type, event.Data)
	ra.c.Writer.Flush()
}
