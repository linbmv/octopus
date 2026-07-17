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
	// firstTokenTimeoutColdStart 用于无健康样本且仍有故障转移余地的流式尝试：
	// 用更激进的首字上限尽快让位给剩余候选，避免死渠道拖满全局默认值。
	firstTokenTimeoutColdStart
	// firstTokenTimeoutNonStreamAttempt 限制单次非流式尝试等待响应头的时长，
	// 仅在存在其他候选时生效；最后一个候选保留完整请求预算。
	firstTokenTimeoutNonStreamAttempt
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
	case firstTokenTimeoutColdStart:
		return "cold_start_first_event_timeout"
	case firstTokenTimeoutNonStreamAttempt:
		return "non_stream_attempt_timeout"
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
	relayConf := conf.Current().Relay
	return selectFirstTokenTimeout(
		ra.group.FirstTokenTimeOut,
		adaptiveTimeout,
		hasAdaptiveTimeout,
		relayConf.StreamFirstEventTimeoutSeconds,
		relayConf.StreamColdStartFirstEventTimeoutSeconds,
		ra.hasFailoverAlternative(),
	)
}

// nonStreamAttemptTimeout 返回单次非流式尝试的响应头等待上限。
// 仅当还有其他候选可以转移时生效——最后一个候选保留完整的
// non_stream_timeout_seconds 预算，避免误杀合法的慢生成。
func (ra *relayAttempt) nonStreamAttemptTimeout() firstTokenTimeoutConfig {
	if ra == nil || !ra.hasFailoverAlternative() {
		return firstTokenTimeoutConfig{}
	}
	seconds := conf.Current().Relay.NonStreamAttemptTimeoutSeconds
	if seconds <= 0 {
		return firstTokenTimeoutConfig{}
	}
	return firstTokenTimeoutConfig{
		Duration: time.Duration(seconds) * time.Second,
		Source:   firstTokenTimeoutNonStreamAttempt,
	}
}

// hasFailoverAlternative 判断当前尝试之后是否仍有候选（同渠道剩余 key、
// 当前分组剩余 item、或上层分组栈的剩余 item）。近似：不含嵌套子分组
// 展开后可能为空的情况，宁可偏向"有备选"，代价只是提前切换一次。
func (ra *relayAttempt) hasFailoverAlternative() bool {
	if ra == nil {
		return false
	}
	if ra.keyIndex+1 < len(ra.keyOptions) {
		return true
	}
	if ra.iter != nil && ra.iter.Index()+1 < ra.iter.Len() {
		return true
	}
	if ra.relayRun != nil {
		for _, frame := range ra.relayRun.iterStack {
			if frame == nil || frame.iter == nil || frame.iter == ra.iter {
				continue
			}
			if frame.iter.Index()+1 < frame.iter.Len() {
				return true
			}
		}
	}
	return false
}

func selectFirstTokenTimeout(manualSeconds int, adaptive time.Duration, hasAdaptive bool, globalSeconds int, coldStartSeconds int, hasAlternative bool) firstTokenTimeoutConfig {
	if manualSeconds > 0 {
		return firstTokenTimeoutConfig{
			Duration: time.Duration(manualSeconds) * time.Second,
			Source:   firstTokenTimeoutManual,
		}
	}
	if hasAdaptive && adaptive > 0 {
		return firstTokenTimeoutConfig{Duration: adaptive, Source: firstTokenTimeoutAdaptive}
	}
	// 冷启动（无手工值也无健康样本）且仍有故障转移余地时收紧首字上限，
	// 让挂死渠道尽快让位；最后的候选回落到全局值保留完整耐心。
	if hasAlternative && coldStartSeconds > 0 && (globalSeconds <= 0 || coldStartSeconds < globalSeconds) {
		return firstTokenTimeoutConfig{
			Duration: time.Duration(coldStartSeconds) * time.Second,
			Source:   firstTokenTimeoutColdStart,
		}
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
