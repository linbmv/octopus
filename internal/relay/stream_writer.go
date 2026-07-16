package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

var errStreamIdleTimeout = errors.New("upstream stream idle timeout")

type streamSoftError struct {
	body []byte
}

func (e *streamSoftError) Error() string {
	return "upstream stream returned an error event"
}

func (e *streamSoftError) Body() []byte {
	if e == nil {
		return nil
	}
	return e.body
}

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
	idleTimeout := time.Duration(conf.Current().Relay.StreamIdleTimeoutSeconds) * time.Second
	return ra.processStreamEvents(ctx, results, &firstToken, responseLog, firstTokenTimeout, stopFirstTokenGuard, closeStream, idleTimeout, ra.streamActivity)
}

type sseReadResult struct {
	event *httpclient.StreamEvent
	err   error
}

// startStreamReader 启动异步读取协程，从 clientStream 读取事件并发送到 results channel
func (ra *relayAttempt) startStreamReader(ctx context.Context, clientStream streams.Stream[*httpclient.StreamEvent], stopFirstTokenGuard func(), closeStream func()) chan sseReadResult {
	results := make(chan sseReadResult, 1)
	go readStream(ctx, clientStream, results, stopFirstTokenGuard, closeStream)

	return results
}

func readStream(ctx context.Context, clientStream streams.Stream[*httpclient.StreamEvent], results chan sseReadResult, stopFirstTokenGuard func(), closeStream func()) {
	defer close(results)
	defer closeStream()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Warnf("stream reader panic: %v", recovered)
			sendStreamResult(ctx, results, sseReadResult{err: fmt.Errorf("stream reader panic: %v", recovered)})
		}
	}()

	readerFirst := true
	for clientStream.Next() {
		event := clientStream.Current()
		if readerFirst && event != nil && len(event.Data) > 0 {
			stopFirstTokenGuard()
			readerFirst = false
		}
		if !sendStreamResult(ctx, results, sseReadResult{event: event}) {
			return
		}
	}
	if err := clientStream.Err(); err != nil {
		sendStreamResult(ctx, results, sseReadResult{err: err})
	}
}

func sendStreamResult(ctx context.Context, results chan<- sseReadResult, result sseReadResult) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

// processStreamEvents 处理流事件的主循环
func (ra *relayAttempt) processStreamEvents(ctx context.Context, results chan sseReadResult, firstToken *bool, responseLog *streamLogCollector, firstTokenTimeout firstTokenTimeoutConfig, stopFirstTokenGuard func(), closeStream func(), idleTimeout time.Duration, rawActivity <-chan struct{}) error {
	finishCompleted := func() error {
		log.Infof("terminal stream event received; treating stream as complete")
		closeStream()
		return ra.handleStreamEnd(context.WithoutCancel(ctx), responseLog)
	}
	var idleTimer *time.Timer
	var idleTimerC <-chan time.Time
	resetIdleTimer := func() {
		if idleTimeout <= 0 {
			return
		}
		if idleTimer == nil {
			idleTimer = time.NewTimer(idleTimeout)
			idleTimerC = idleTimer.C
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(idleTimeout)
	}
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	for {
		select {
		case <-rawActivity:
			// This includes raw SSE comment heartbeats that a decoder may consume
			// without yielding a StreamEvent. The guard remains unarmed until the
			// first non-empty event has reached the relay.
			if !*firstToken {
				resetIdleTimer()
			}

		case <-idleTimerC:
			if responseLog.Completed() {
				return finishCompleted()
			}
			closeStream()
			return fmt.Errorf("%w after %s", errStreamIdleTimeout, idleTimeout)

		case <-ctx.Done():
			// Codex and other SSE clients commonly close the connection as soon as
			// they receive the protocol terminal event. At that point the response
			// is complete even if the reader has not yet observed the upstream EOF.
			if responseLog.Completed() {
				return finishCompleted()
			}
			return ra.handleContextDone(ctx, *firstToken, firstTokenTimeout, closeStream)

		case r, ok := <-results:
			if !ok {
				if responseLog.Completed() {
					return finishCompleted()
				}
				if ctx.Err() != nil {
					return ra.handleContextDone(ctx, *firstToken, firstTokenTimeout, closeStream)
				}
				return ra.handleStreamEnd(ctx, responseLog)
			}
			if r.err != nil {
				if responseLog.Completed() {
					return finishCompleted()
				}
				if ctx.Err() != nil {
					return ra.handleContextDone(ctx, *firstToken, firstTokenTimeout, closeStream)
				}
				log.Warnf("failed to read event: %v", r.err)
				return fmt.Errorf("failed to read stream event: %w", r.err)
			}
			if r.event == nil {
				continue
			}
			if !*firstToken {
				resetIdleTimer()
			}

			// 保存事件到日志收集器
			responseLog.Add(r.event)
			if len(r.event.Data) == 0 {
				continue
			}
			if body, isError := streamErrorEventBody(r.event); isError {
				// Detect before writing the error event to the client. If it is the
				// first event the normal relay retry path is still safe; if content
				// was already written, the attempt is still classified/persisted as
				// failed but run() correctly suppresses a duplicate retry.
				closeStream()
				ra.metrics.InternalResponse = append([]byte(nil), body...)
				return &streamSoftError{body: body}
			}

			// 处理首 token
			if *firstToken {
				ra.handleFirstToken(stopFirstTokenGuard)
				*firstToken = false
				resetIdleTimer()
			}

			// 写入客户端
			ra.writeEventToClient(r.event)
		}
	}
}

func streamErrorEventBody(event *httpclient.StreamEvent) ([]byte, bool) {
	if event == nil || len(event.Data) == 0 {
		return nil, false
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if eventType == "error" || eventType == "response.failed" || eventType == "message_error" {
		body := make([]byte, 0, len(event.Data)+24)
		body = append(body, "event: error\ndata: "...)
		body = append(body, event.Data...)
		body = append(body, '\n', '\n')
		classification := errorclass.ClassifyResponse(http.StatusOK, nil, body, "text/event-stream")
		return body, classification.Level != errorclass.ErrorLevelNone
	}
	classification := errorclass.ClassifyResponse(http.StatusOK, nil, event.Data, "application/json")
	if classification.Level != errorclass.ErrorLevelNone {
		return append([]byte(nil), event.Data...), true
	}
	return nil, false
}
