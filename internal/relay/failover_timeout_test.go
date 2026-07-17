package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// newHungUpstream 返回一个接受连接但永不返回响应头的上游，
// 模拟"挂而不拒"的坏渠道——故障转移收敛的最坏情况。
// 用独立的 serverDone 通道让 handler 在测试清理时可靠退出，
// 避免 httptest.Server.Close() 因 handler 仍阻塞而死锁。
func newHungUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	serverDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // 客户端放弃
		case <-serverDone: // 测试清理
		}
	}))
	t.Cleanup(func() {
		close(serverDone)
		upstream.Close()
	})
	return upstream
}

// newNonStreamAttempt 构造一个指向 upstream 的非流式 chat 尝试。
// keyCount 控制 keyOptions 数量：>1 表示存在故障转移余地。
func newNonStreamAttempt(t *testing.T, upstreamURL string, keyCount int) *relayAttempt {
	t.Helper()
	gin.SetMode(gin.TestMode)

	channel := &dbmodel.Channel{
		ID:       1,
		Name:     "hung-channel",
		Type:     llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []dbmodel.BaseUrl{{URL: upstreamURL + "/v1"}},
	}
	internalRequest := &llm.Request{
		Model:       "test-model",
		APIFormat:   llm.APIFormatOpenAIChatCompletion,
		RequestType: llm.RequestTypeChat,
		Messages: []llm.Message{
			compactInputMessage("user", "hi"),
		},
		RawRequest: &httpclient.Request{
			Method:  http.MethodPost,
			Path:    "/v1/chat/completions",
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
		},
	}
	outAdapter, err := newOutbound(channel.Type, internalRequest, channel.GetBaseUrl(), "test-key")
	if err != nil {
		t.Fatalf("newOutbound returned error: %v", err)
	}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	requestCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // 测试结束时释放仍挂着的上游连接
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestCtx)

	keys := make([]dbmodel.ChannelKey, keyCount)
	for i := range keys {
		keys[i] = dbmodel.ChannelKey{ID: i + 1, ChannelKey: "test-key"}
	}

	return &relayAttempt{
		relayRun: &relayRun{
			c:               ginCtx,
			inAdapter:       newInbound(llm.APIFormatOpenAIChatCompletion),
			internalRequest: internalRequest,
			metrics:         &RelayMetrics{ActualModel: internalRequest.Model},
		},
		outAdapter: outAdapter,
		channel:    channel,
		usedKey:    keys[0],
		keyOptions: keys,
		keyIndex:   0,
	}
}

func setNonStreamAttemptTimeout(t *testing.T, seconds int) {
	t.Helper()
	old := conf.Current()
	config := conf.Current()
	config.Relay.NonStreamAttemptTimeoutSeconds = seconds
	if err := conf.Set(config); err != nil {
		t.Fatalf("conf.Set() error = %v", err)
	}
	t.Cleanup(func() {
		if err := conf.Set(old); err != nil {
			t.Fatalf("conf.Set() restore error = %v", err)
		}
	})
}

// TestNonStreamAttemptTimeoutFailsOverFromHungUpstream 是故障转移收敛的
// 度量夹具：修复前（守卫禁用即基线行为）挂死上游会占用整个
// non_stream_timeout_seconds 全局预算；修复后 per-attempt 守卫在配置秒数
// 内放弃该候选，让位给剩余渠道。
func TestNonStreamAttemptTimeoutFailsOverFromHungUpstream(t *testing.T) {
	upstream := newHungUpstream(t)
	setNonStreamAttemptTimeout(t, 1)
	ra := newNonStreamAttempt(t, upstream.URL, 2) // 还有第 2 个 key 可转移

	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, _, _, err := ra.forward()
		done <- result{err: err, elapsed: time.Since(start)}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("挂死上游必须返回错误")
		}
		var timeoutErr *firstTokenTimeoutError
		if !errors.As(r.err, &timeoutErr) || timeoutErr.config.Source != firstTokenTimeoutNonStreamAttempt {
			t.Fatalf("错误应为非流式 attempt 超时，got %v", r.err)
		}
		if r.elapsed >= 5*time.Second {
			t.Fatalf("守卫应在约 1s 放弃挂死渠道，实际 %v", r.elapsed)
		}
		// 分类必须是 channel 级，driving runner 切换下一候选。
		decision := decideRelayError(0, nil, nil, r.err)
		if decision.Action != ErrorActionRetryChannel {
			t.Fatalf("非流式 attempt 超时应触发渠道级重试，got action=%v", decision.Action)
		}
		if decision.Classification.Reason != "non-stream attempt timeout" {
			t.Fatalf("分类 reason = %q", decision.Classification.Reason)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("per-attempt 守卫未生效：请求仍挂在死渠道上（基线缺陷行为）")
	}
}

// TestNonStreamAttemptTimeoutSkippedForLastCandidate 验证语义：没有故障
// 转移余地（唯一 key、无剩余候选）时不启用 per-attempt 守卫，
// 最后的候选保留完整全局预算，避免误杀合法慢生成。
func TestNonStreamAttemptTimeoutSkippedForLastCandidate(t *testing.T) {
	upstream := newHungUpstream(t)
	setNonStreamAttemptTimeout(t, 1)
	ra := newNonStreamAttempt(t, upstream.URL, 1) // 唯一 key，无备选

	if got := ra.nonStreamAttemptTimeout(); got.Duration != 0 {
		t.Fatalf("最后候选不应启用 per-attempt 守卫，got %v", got.Duration)
	}

	// 行为验证：1.5s 内 forward 不应因 attempt 守卫返回。
	done := make(chan error, 1)
	go func() {
		_, _, _, err := ra.forward()
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("无备选时不应触发 per-attempt 超时，但返回了: %v", err)
	case <-time.After(1500 * time.Millisecond):
		// 预期：仍在等待（真实部署中由全局 non_stream_timeout_seconds 兜底）。
	}
}

func TestHasFailoverAlternative(t *testing.T) {
	ra := &relayAttempt{relayRun: &relayRun{}}
	if ra.hasFailoverAlternative() {
		t.Fatal("无 key、无迭代器时应为 false")
	}
	ra.keyOptions = make([]dbmodel.ChannelKey, 2)
	ra.keyIndex = 0
	if !ra.hasFailoverAlternative() {
		t.Fatal("同渠道还有下一个 key 时应为 true")
	}
	ra.keyIndex = 1
	if ra.hasFailoverAlternative() {
		t.Fatal("最后一个 key 且无候选时应为 false")
	}
}

// TestShouldRecordURLFailureFiltersByErrorClass 锁定 URL 冷却语义：
// 只有通道级失败才是端点状态的证据。
func TestShouldRecordURLFailureFiltersByErrorClass(t *testing.T) {
	// 401 → key 级：换 URL 也一样，不应记 URL 冷却。
	keyLevel := decideRelayError(401, nil, []byte(`{"error":{"message":"invalid api key"}}`), errors.New("upstream 401"))
	if shouldRecordURLFailure(keyLevel) {
		t.Fatalf("key 级错误不应记 URL 冷却, classification=%+v", keyLevel.Classification)
	}

	// 纯网络/传输故障（无 HTTP 状态）→ 通道级：应记 URL 冷却。
	channelLevel := decideRelayError(0, nil, nil, errors.New("dial tcp: connection refused"))
	if !shouldRecordURLFailure(channelLevel) {
		t.Fatalf("通道级错误应记 URL 冷却, classification=%+v", channelLevel.Classification)
	}

	// 非流式 attempt 超时（挂死端点）→ 通道级：应记 URL 冷却。
	timeoutErr := firstTokenTimeoutConfig{Duration: time.Second, Source: firstTokenTimeoutNonStreamAttempt}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	timeoutDecision := decideRelayError(0, nil, nil, timeoutErr)
	if !shouldRecordURLFailure(timeoutDecision) {
		t.Fatalf("attempt 超时应记 URL 冷却, classification=%+v", timeoutDecision.Classification)
	}
}
