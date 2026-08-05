package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

func (r *relayRun) run() {
	ctx := r.c.Request.Context()
	defer r.releaseDeferredSlowAttempts()
	var lastErr error
	var lastClientErr *classifiedClientRelayError

	for {
		select {
		case <-ctx.Done():
			if isNonStreamRequestTimeout(ctx) {
				timeoutErr := newTerminalRelayError(http.StatusGatewayTimeout, errNonStreamRequestTimeout)
				log.Infof("non-streaming request deadline reached, stopping retry")
				r.metrics.Save(context.WithoutCancel(ctx), false, timeoutErr, r.attempts())
				if !r.c.Writer.Written() {
					respondRelayError(r.c, http.StatusGatewayTimeout, timeoutErr)
				}
				return
			}
			log.Infof("request context canceled, stopping retry")
			r.metrics.Save(ctx, r.c.Writer.Written(), nil, r.attempts())
			return
		default:
		}
		if r.routingChanged() {
			if err := r.refreshRouting(); err != nil {
				var terminalErr *terminalRelayError
				if errors.As(err, &terminalErr) {
					r.metrics.Save(ctx, false, err, r.attempts())
					respondRelayError(r.c, terminalErr.StatusCode(), terminalErr)
					return
				}
				lastErr = err
				break
			}
			continue
		}

		attempt, err := r.prepareAttempt()
		if err != nil {
			var terminalErr *terminalRelayError
			if errors.As(err, &terminalErr) {
				r.metrics.Save(ctx, false, err, r.attempts())
				respondRelayError(r.c, terminalErr.StatusCode(), terminalErr)
				return
			}
			lastErr = err
			var clientErr *classifiedClientRelayError
			if errors.As(err, &clientErr) {
				lastClientErr = clientErr
			}
			continue
		}
		if attempt == nil {
			break
		}

		written, err := r.runAttempt(attempt)
		if err == nil {
			r.requestFailoverState().stop()
			r.metrics.Save(ctx, true, nil, r.attempts())
			return
		}
		if errors.Is(err, errRoutingConfigChanged) && !written {
			attempt.exhaustRequestCandidate(requestFailureChannel)
			if refreshErr := r.refreshRouting(); refreshErr != nil {
				var terminalErr *terminalRelayError
				if errors.As(refreshErr, &terminalErr) {
					r.metrics.Save(ctx, false, refreshErr, r.attempts())
					respondRelayError(r.c, terminalErr.StatusCode(), terminalErr)
					return
				}
				lastErr = refreshErr
				break
			}
			continue
		}
		var terminalErr *terminalRelayError
		if errors.As(err, &terminalErr) {
			r.metrics.Save(ctx, false, err, r.attempts())
			respondRelayError(r.c, terminalErr.StatusCode(), terminalErr)
			return
		}
		if written {
			if isRequestContextCanceled(ctx, err) {
				r.metrics.Save(ctx, true, nil, r.attempts())
				return
			}
			r.metrics.Save(ctx, false, err, r.attempts())
			return
		}
		if r.runAttemptFunc != nil {
			attempt.exhaustRequestCandidate(requestFailureChannel)
		}
		lastErr = err
		var clientErr *classifiedClientRelayError
		if errors.As(err, &clientErr) {
			lastClientErr = clientErr
		}
	}

	if lastErr == nil {
		lastErr = errors.New("all channels failed")
	}
	if lastClientErr != nil {
		r.metrics.Save(ctx, false, lastClientErr, r.attempts())
		respondRelayError(r.c, lastClientErr.StatusCode(), lastClientErr)
		return
	}
	r.metrics.Save(ctx, false, lastErr, r.attempts())
	respondRelayError(r.c, http.StatusBadGateway, lastErr)
}

const (
	relayUpstreamErrorCode    = "UPSTREAM_ERROR"
	relayUpstreamErrorMessage = "Upstream service unavailable"
)

// respondRelayError keeps actionable client-side errors intact while ensuring
// transport, channel, and internal details never cross the public 5xx boundary.
// The original error remains available in structured server logs for diagnosis.
func respondRelayError(c *gin.Context, status int, err error) {
	if status < http.StatusInternalServerError {
		message := http.StatusText(status)
		if err != nil && err.Error() != "" {
			message = err.Error()
		}
		resp.Error(c, status, message)
		return
	}

	log.WithContext(c.Request.Context()).Errorw(
		"relay request failed",
		"status_code", status,
		"error", err,
	)
	resp.ErrorWithCode(c, status, relayUpstreamErrorCode, relayUpstreamErrorMessage)
}

func isNonStreamRequestTimeout(ctx context.Context) bool {
	return ctx != nil && errors.Is(context.Cause(ctx), errNonStreamRequestTimeout)
}

func (r *relayRun) attempts() []dbmodel.ChannelAttempt {
	if r.timeline != nil {
		return append([]dbmodel.ChannelAttempt(nil), r.timeline...)
	}
	attempts := make([]dbmodel.ChannelAttempt, 0)
	for _, iter := range r.iterHistory {
		attempts = append(attempts, iter.Attempts()...)
	}
	for i := range attempts {
		attempts[i].AttemptNum = i + 1
	}
	return attempts
}

func (r *relayRun) attachIteratorTimeline(iter *balancer.Iterator) {
	if r == nil || iter == nil {
		return
	}
	if r.timeline == nil {
		r.timeline = make([]dbmodel.ChannelAttempt, 0)
	}
	iter.SetAttemptEventSink(func(attempt dbmodel.ChannelAttempt) {
		attempt.AttemptNum = len(r.timeline) + 1
		r.timeline = append(r.timeline, attempt)
	})
}

func (r *relayRun) runAttempt(attempt *relayAttempt) (bool, error) {
	if r.runAttemptFunc != nil {
		return r.runAttemptFunc(attempt)
	}
	return attempt.run()
}

func (r *relayRun) prepareAttempt() (*relayAttempt, error) {
	for {
		if err := r.c.Request.Context().Err(); err != nil {
			return nil, err
		}
		frame := r.currentIteratorFrame()
		if frame == nil {
			attempt := r.popDeferredSlowAttempt()
			if attempt == nil {
				return nil, nil
			}
			if !r.selectRequestCandidate(attempt) {
				balancer.RecordProbeAbort(attempt.channel.ID, attempt.usedKey.ID, attempt.groupItem.ModelName)
				attempt.releaseSlowRecoveryLease()
				continue
			}
			return attempt, nil
		}
		item := frame.iter.Item()
		if item.Type != dbmodel.GroupItemTypeGroup {
			r.iter = frame.iter
			attempt, err := r.resolveCandidate(item, frame.iter.IsSticky(), frame.iter.StickyKeyID())
			if attempt != nil {
				attempt.firstTokenPolicyGroup = &frame.group
				if attempt.slowRecoveryLease != 0 && attempt.hasFailoverAlternative() {
					r.deferSlowAttempt(attempt)
					continue
				}
				if !r.selectRequestCandidate(attempt) {
					if attempt.channel != nil {
						balancer.RecordProbeAbort(attempt.channel.ID, attempt.usedKey.ID, item.ModelName)
					}
					continue
				}
			}
			if err != nil || attempt != nil {
				return attempt, err
			}
			continue
		}
		if err := r.pushNestedGroupIterator(frame, item); err != nil {
			return nil, err
		}
	}
}

// deferSlowAttempt postpones an already-known slow candidate until ordinary
// candidates have been exhausted. The stored attempt has not contacted the
// upstream; its dedicated iterator is used only if a later real user request
// step reaches this passive recovery fallback.
func (r *relayRun) deferSlowAttempt(attempt *relayAttempt) {
	if r == nil || attempt == nil || attempt.channel == nil {
		if attempt != nil {
			attempt.releaseSlowRecoveryLease()
		}
		return
	}
	group := r.group
	if attempt.firstTokenPolicyGroup != nil {
		group = *attempt.firstTokenPolicyGroup
	}
	group.Items = []dbmodel.GroupItem{attempt.groupItem}
	// buildRealAttempt may have acquired a circuit HalfOpen slot. Deferral does
	// not contact the upstream, so return it now and acquire afresh when this
	// real-request fallback is actually selected.
	balancer.RecordProbeAbort(attempt.channel.ID, attempt.usedKey.ID, attempt.groupItem.ModelName)
	iter := balancer.NewIteratorFromCandidates(
		group,
		r.metrics.APIKeyID,
		r.metrics.RequestModel,
		[]dbmodel.GroupItem{attempt.groupItem},
		nil,
	)
	if !iter.Next() {
		attempt.releaseSlowRecoveryLease()
		return
	}
	r.attachIteratorTimeline(iter)
	r.iterHistory = append(r.iterHistory, iter)
	// Keep the channel's remaining URL/key fallbacks attached. The known-slow
	// candidate is delayed behind ordinary group items, but deferral must never
	// make an otherwise eligible same-channel fallback disappear.
	r.deferredSlowAttempts = append(r.deferredSlowAttempts, deferredSlowAttempt{attempt: attempt, iter: iter})
}

func (r *relayRun) popDeferredSlowAttempt() *relayAttempt {
	for r != nil && len(r.deferredSlowAttempts) > 0 {
		deferred := r.deferredSlowAttempts[0]
		r.deferredSlowAttempts = r.deferredSlowAttempts[1:]
		attempt := deferred.attempt
		if attempt == nil || attempt.channel == nil || deferred.iter == nil {
			if attempt != nil {
				attempt.releaseSlowRecoveryLease()
			}
			continue
		}
		if !r.requestCandidateAllowed(attempt.channel, attempt.usedKey, attempt.groupItem.ModelName, attempt.baseURL) {
			attempt.releaseSlowRecoveryLease()
			continue
		}
		r.iter = deferred.iter
		if r.iter.SkipCircuitBreak(attempt.channel.ID, attempt.usedKey.ID, attempt.channel.Name, attempt.keyRemark) {
			attempt.releaseSlowRecoveryLease()
			continue
		}
		r.internalRequest.Model = attempt.groupItem.ModelName
		r.metrics.ActualModel = attempt.groupItem.ModelName
		r.metrics.ParamOverride = ""
		r.metrics.OutboundRequestSummary = nil
		r.metrics.OutboundRequestArtifact = nil
		attempt.responseHeaderDuration = 0
		outAdapter, err := newChannelOutbound(attempt.channel, r.internalRequest, attempt.baseURL, attempt.usedKey)
		if err != nil {
			balancer.RecordProbeAbort(attempt.channel.ID, attempt.usedKey.ID, attempt.groupItem.ModelName)
			r.iter.Skip(attempt.channel.ID, attempt.usedKey.ID, attempt.channel.Name, err.Error())
			attempt.releaseSlowRecoveryLease()
			continue
		}
		attempt.outAdapter = outAdapter
		attempt.selectionReason = "passive_slow_recovery"
		return attempt
	}
	return nil
}

func (r *relayRun) releaseDeferredSlowAttempts() {
	if r == nil {
		return
	}
	for _, deferred := range r.deferredSlowAttempts {
		if deferred.attempt != nil {
			deferred.attempt.releaseSlowRecoveryLease()
		}
	}
	r.deferredSlowAttempts = nil
}

func (r *relayRun) resolveCandidate(
	item dbmodel.GroupItem,
	sticky bool,
	stickyKeyID int,
) (*relayAttempt, error) {
	if r.resolveGroupItemFunc != nil {
		return r.resolveGroupItemFunc(item, sticky, stickyKeyID)
	}
	return r.resolveGroupItem(item, sticky, stickyKeyID)
}

func (r *relayRun) currentIteratorFrame() *relayIteratorFrame {
	for len(r.iterStack) > 0 {
		frame := r.iterStack[len(r.iterStack)-1]
		if frame.iter.Next() {
			r.iter = frame.iter
			return frame
		}
		r.iterStack = r.iterStack[:len(r.iterStack)-1]
		if len(r.iterStack) > 0 {
			r.iter = r.iterStack[len(r.iterStack)-1].iter
		}
	}
	return nil
}

func (r *relayRun) pushNestedGroupIterator(parent *relayIteratorFrame, item dbmodel.GroupItem) error {
	// 写入侧已有防环校验（自引用/图环检测/深度上限），这里是运行时纵深防御：
	// 脏数据成环时每层都会以全新 visited 集重新展开，必须靠深度上限阻断无限 push。
	if parent.depth+1 > op.MaxGroupNestDepth {
		parent.iter.SkipFor(item, false, 0, 0, fmt.Sprintf("group_%d", item.TargetGroupID),
			fmt.Sprintf("nested group depth exceeded (max %d)", op.MaxGroupNestDepth))
		return nil
	}
	targetGroup, err := op.GroupGetEnabledTreeByID(item.TargetGroupID, r.c.Request.Context())
	if err != nil {
		parent.iter.SkipFor(item, false, 0, 0, fmt.Sprintf("group_%d", item.TargetGroupID), err.Error())
		return nil // Skip failed nested group and continue iteration
	}
	if len(targetGroup.Items) == 0 {
		parent.iter.SkipFor(item, false, 0, 0, targetGroup.Name, "nested group has no available channel")
		return nil
	}
	parent.iter.RedirectFor(item, 0, parent.group.Name, targetGroup.ID, targetGroup.Name, parent.depth+1, "enter nested group")
	childIter := newRelayIteratorWithSessionAndBaseURLs(*targetGroup, r.c.GetInt("api_key_id"), r.internalRequest, r.c.Request.Context(), r.selectedBaseURLs, r.sessionID)
	r.attachIteratorTimeline(childIter)
	r.iterHistory = append(r.iterHistory, childIter)
	r.iterStack = append(r.iterStack, &relayIteratorFrame{
		group: *targetGroup,
		iter:  childIter,
		depth: parent.depth + 1,
	})
	return nil
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
	if remaining := channelRateLimitRemaining(channel.ID); remaining > 0 {
		r.iter.Skip(channel.ID, 0, channel.Name, fmt.Sprintf("channel rate limited, retry after %ds", max(1, int(remaining.Seconds()))))
		return nil, nil
	}

	baseURLOptions := baseURLCandidatesForChannel(channel, r.selectedBaseURLs)
	baseURL := ""
	if len(baseURLOptions) > 0 {
		baseURL = baseURLOptions[0]
	} else {
		baseURL = selectedBaseURLForChannel(channel, r.selectedBaseURLs)
		if baseURL != "" {
			baseURLOptions = []string{baseURL}
		}
	}
	keyOptions := channel.AvailableKeysForAttempt(stickyKeyID)
	keyOptions = op.RankChannelKeysByCapability(
		r.c.Request.Context(), channel, keyOptions, item.ModelName,
		dbmodel.RequiredCapabilities(r.internalRequest), baseURL,
	)
	if len(keyOptions) == 0 {
		r.iter.Skip(channel.ID, 0, channel.Name, "no available key")
		return nil, nil
	}

	for keyIndex, usedKey := range keyOptions {
		candidateBaseURL := baseURL
		candidateBaseURLIndex := 0
		if len(baseURLOptions) > 0 {
			candidateBaseURL = ""
			candidateBaseURLIndex = -1
			for i, option := range baseURLOptions {
				if option != "" && r.requestCandidateAllowed(channel, usedKey, item.ModelName, option) {
					candidateBaseURL = option
					candidateBaseURLIndex = i
					break
				}
			}
		} else if !r.requestCandidateAllowed(channel, usedKey, item.ModelName, candidateBaseURL) {
			continue
		}
		if candidateBaseURLIndex < 0 {
			continue
		}
		keyRemark := cleanKeyRemark(usedKey.Remark)
		if r.iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name, keyRemark) {
			continue
		}
		slowKey := newSlowRecoveryKey(channel, usedKey, item.ModelName, candidateBaseURL)
		allowed, slowLease, remaining := globalSlowRecovery.acquireForBudget(
			slowKey,
			channelInitialResponseTimeoutBudget(channel),
		)
		if !allowed {
			// Slow recovery is passive: skip this candidate until another real
			// request reaches its due time. No synthetic upstream request is made.
			balancer.RecordProbeAbort(channel.ID, usedKey.ID, item.ModelName)
			r.iter.Skip(channel.ID, usedKey.ID, channel.Name, slowRecoveryBackoffMessage(remaining))
			continue
		}
		outAdapter, err := newChannelOutbound(channel, r.internalRequest, candidateBaseURL, usedKey)
		if err != nil {
			if slowLease != 0 {
				globalSlowRecovery.release(slowKey, slowLease)
			}
			// SkipCircuitBreak 通过时可能已把该 key 置为半开试探者；此处未发出
			// 真实请求即放弃，必须归还试探名额，否则半开态会滞留到租约超时。
			balancer.RecordProbeAbort(channel.ID, usedKey.ID, item.ModelName)
			r.iter.Skip(channel.ID, usedKey.ID, channel.Name, err.Error())
			continue
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
			relayRun:          r,
			outAdapter:        outAdapter,
			channel:           channel,
			groupItem:         item,
			usedKey:           usedKey,
			keyOptions:        keyOptions,
			keyIndex:          keyIndex,
			baseURL:           candidateBaseURL,
			baseURLOptions:    baseURLOptions,
			baseURLIndex:      candidateBaseURLIndex,
			attemptAction:     "selected",
			selectionReason:   "runtime_url_selector",
			keyRemark:         keyRemark,
			slowRecoveryKey:   slowKey,
			slowRecoveryLease: slowLease,
		}, nil
	}

	return nil, nil
}
