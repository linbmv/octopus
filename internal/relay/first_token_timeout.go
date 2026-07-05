package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

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
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, errFirstTokenTimeout.Error()) ||
		strings.Contains(msg, "first_token_timeout") ||
		strings.Contains(msg, "manual_first_token_timeout") ||
		strings.Contains(msg, "auto_first_token_timeout")
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
