package relay

import (
	"context"
	"fmt"
	"sync"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

// writeStream 把 pipeline 输出的客户端格式流写回请求方，并保留首 token 超时切换通道的行为。
// stopFirstTokenGuard 在收到首个 token 后调用,停止 forward 阶段建立的首字超时计时器。
func (ra *relayAttempt) writeStream(ctx context.Context, stopFirstTokenGuard func(), firstTokenTimeout firstTokenTimeoutConfig, clientStream streams.Stream[*httpclient.StreamEvent]) error {
	if clientStream == nil {
		return fmt.Errorf("empty pipeline stream")
	}
	if stopFirstTokenGuard == nil {
		stopFirstTokenGuard = func() {}
	}

	// reader 协程退出与主循环的超时/断开路径都会关闭流，且第三方 Stream 的
	// Close 不保证线程安全：用 sync.Once 收敛为单次调用。
	var closeOnce sync.Once
	closeStream := func() {
		closeOnce.Do(func() {
			if err := clientStream.Close(); err != nil {
				log.Debugf("close client stream: %v", err)
			}
		})
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

	// 启动异步读取协程
	results := ra.startStreamReader(ctx, clientStream, stopFirstTokenGuard, closeStream)

	// 主事件处理循环
	return ra.processStreamEvents(ctx, results, &firstToken, responseLog, firstTokenTimeout, stopFirstTokenGuard, closeStream)
}

type sseReadResult struct {
	event *httpclient.StreamEvent
	err   error
}

// startStreamReader 启动异步读取协程，从 clientStream 读取事件并发送到 results channel
func (ra *relayAttempt) startStreamReader(ctx context.Context, clientStream streams.Stream[*httpclient.StreamEvent], stopFirstTokenGuard func(), closeStream func()) chan sseReadResult {
	results := make(chan sseReadResult, 1)
	done := make(chan struct{})

	go func() {
		defer close(results)
		defer closeStream()
		defer close(done)
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
			// 收到首个有效事件立刻停掉首字超时计时器
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

	return results
}

// processStreamEvents 处理流事件的主循环
func (ra *relayAttempt) processStreamEvents(ctx context.Context, results chan sseReadResult, firstToken *bool, responseLog *streamLogCollector, firstTokenTimeout firstTokenTimeoutConfig, stopFirstTokenGuard func(), closeStream func()) error {
	for {
		select {
		case <-ctx.Done():
			return ra.handleContextDone(ctx, *firstToken, firstTokenTimeout, closeStream)

		case r, ok := <-results:
			if !ok {
				return ra.handleStreamEnd(ctx, responseLog)
			}
			if r.err != nil {
				log.Warnf("failed to read event: %v", r.err)
				return fmt.Errorf("failed to read stream event: %w", r.err)
			}
			if r.event == nil || len(r.event.Data) == 0 {
				continue
			}

			// 保存事件到日志收集器
			responseLog.Add(r.event)

			// 处理首 token
			if *firstToken {
				ra.handleFirstToken(stopFirstTokenGuard)
				*firstToken = false
			}

			// 写入客户端
			ra.writeEventToClient(r.event)
		}
	}
}
