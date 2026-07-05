package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

// writeStream 把 pipeline 输出的客户端格式流写回请求方，并保留首 token 超时切换通道的行为。
// stopFirstTokenGuard 在收到首个 token 后调用，停止 forward 阶段建立的首字超时计时器。
func (ra *relayAttempt) writeStream(ctx context.Context, stopFirstTokenGuard func(), firstTokenTimeout firstTokenTimeoutConfig, clientStream streams.Stream[*httpclient.StreamEvent]) error {
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
	responseLog := newStreamLogCollector()
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

	for {
		select {
		case <-ctx.Done():
			// 首字超时在收到首个 token 前触发：返回错误以切换下一通道。
			// 客户端断开不是上游成功，必须向外返回取消错误，避免刷新 sticky、熔断成功态和健康成功样本。
			if firstToken && errors.Is(context.Cause(ctx), errFirstTokenTimeout) {
				timeoutErr := firstTokenTimeout.Error(firstTokenTimeoutPhaseStreamFirstEvent)
				log.Warnf("%v, switching channel", timeoutErr)
				ra.recordFirstTokenTimeout(firstTokenTimeout)
				_ = clientStream.Close()
				return timeoutErr
			}
			log.Infof("client disconnected, stopping stream")
			_ = clientStream.Close()
			return context.Canceled
		case r, ok := <-results:
			if !ok {
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
			if r.err != nil {
				log.Warnf("failed to read event: %v", r.err)
				return fmt.Errorf("failed to read stream event: %w", r.err)
			}

			if r.event == nil || len(r.event.Data) == 0 {
				continue
			}
			// 这里只保存有限大小的客户端格式事件，正常短流结束后聚合成最终响应体用于日志；长流超过上限后只保留截断内容。
			event := r.event
			responseLog.Add(event)
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
