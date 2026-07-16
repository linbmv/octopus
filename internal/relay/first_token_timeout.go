package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
)

// errFirstTokenTimeout 标记首字超时触发的 context 取消原因，用于和客户端断开等其他取消区分。
var errFirstTokenTimeout = errors.New("first token timeout")

type firstTokenTimeoutSource int

const (
	firstTokenTimeoutDisabled firstTokenTimeoutSource = iota
	firstTokenTimeoutManual
	firstTokenTimeoutAdaptive
	firstTokenTimeoutGlobal
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
	case firstTokenTimeoutGlobal:
		return "global_first_event_timeout"
	default:
		return "first_token_timeout"
	}
}

type firstTokenTimeoutError struct {
	config firstTokenTimeoutConfig
	phase  firstTokenTimeoutPhase
}

func (e *firstTokenTimeoutError) Error() string {
	if e == nil {
		return errFirstTokenTimeout.Error()
	}
	return fmt.Sprintf("%s:%s (%ds)", e.config.Reason(), e.phase, int(e.config.Duration.Seconds()))
}

func (e *firstTokenTimeoutError) Unwrap() error {
	return errFirstTokenTimeout
}

func (c firstTokenTimeoutConfig) Error(phase firstTokenTimeoutPhase) error {
	return &firstTokenTimeoutError{config: c, phase: phase}
}

func isFirstTokenTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var timeoutErr *firstTokenTimeoutError
	if errors.As(err, &timeoutErr) || errors.Is(err, errFirstTokenTimeout) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, errFirstTokenTimeout.Error()) ||
		strings.Contains(msg, "first_token_timeout") ||
		strings.Contains(msg, "manual_first_token_timeout") ||
		strings.Contains(msg, "auto_first_token_timeout") ||
		strings.Contains(msg, "global_first_event_timeout")
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
	if ra == nil {
		return firstTokenTimeoutConfig{}
	}
	adaptiveTimeout := time.Duration(0)
	hasAdaptiveTimeout := false
	if ra.channel != nil && ra.metrics != nil && smartHealthEnabled() && healthManager != nil && healthManager.HasAdaptiveTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel) {
		adaptiveTimeout = healthManager.GetTimeout(ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel)
		hasAdaptiveTimeout = adaptiveTimeout > 0
	}
	return selectFirstTokenTimeout(
		ra.group.FirstTokenTimeOut,
		adaptiveTimeout,
		hasAdaptiveTimeout,
		conf.Current().Relay.StreamFirstEventTimeoutSeconds,
	)
}

func selectFirstTokenTimeout(manualSeconds int, adaptive time.Duration, hasAdaptive bool, globalSeconds int) firstTokenTimeoutConfig {
	if manualSeconds > 0 {
		return firstTokenTimeoutConfig{
			Duration: time.Duration(manualSeconds) * time.Second,
			Source:   firstTokenTimeoutManual,
		}
	}
	if hasAdaptive && adaptive > 0 {
		return firstTokenTimeoutConfig{Duration: adaptive, Source: firstTokenTimeoutAdaptive}
	}
	if globalSeconds > 0 {
		return firstTokenTimeoutConfig{
			Duration: time.Duration(globalSeconds) * time.Second,
			Source:   firstTokenTimeoutGlobal,
		}
	}
	return firstTokenTimeoutConfig{}
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
	var timeoutErr *firstTokenTimeoutError
	return errors.As(err, &timeoutErr) && timeoutErr.config.Source == firstTokenTimeoutAdaptive
}
