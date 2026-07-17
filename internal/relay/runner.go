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
	var lastErr error

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

		attempt, err := r.prepareAttempt()
		if err != nil {
			var terminalErr *terminalRelayError
			if errors.As(err, &terminalErr) {
				r.metrics.Save(ctx, false, err, r.attempts())
				respondRelayError(r.c, terminalErr.StatusCode(), terminalErr)
				return
			}
			lastErr = err
			continue
		}
		if attempt == nil {
			break
		}

		written, err := attempt.run()
		if err == nil {
			r.metrics.Save(ctx, true, nil, r.attempts())
			return
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
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("all channels failed")
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
	attempts := make([]dbmodel.ChannelAttempt, 0)
	for _, iter := range r.iterHistory {
		attempts = append(attempts, iter.Attempts()...)
	}
	for i := range attempts {
		attempts[i].AttemptNum = i + 1
	}
	return attempts
}

func (r *relayRun) prepareAttempt() (*relayAttempt, error) {
	for {
		if err := r.c.Request.Context().Err(); err != nil {
			return nil, err
		}
		frame := r.currentIteratorFrame()
		if frame == nil {
			return nil, nil
		}
		item := frame.iter.Item()
		if item.Type != dbmodel.GroupItemTypeGroup {
			r.iter = frame.iter
			attempt, err := r.resolveCandidate(item, frame.iter.IsSticky(), frame.iter.StickyKeyID())
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
	childIter := newRelayIteratorWithBaseURLs(*targetGroup, r.c.GetInt("api_key_id"), r.internalRequest, r.c.Request.Context(), r.selectedBaseURLs)
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

	baseURL := selectedBaseURLForChannel(channel, r.selectedBaseURLs)
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
		keyRemark := cleanKeyRemark(usedKey.Remark)
		if r.iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name, keyRemark) {
			continue
		}
		outAdapter, err := newOutbound(channel.Type, r.internalRequest, baseURL, usedKey.ChannelKey)
		if err != nil {
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
			relayRun:   r,
			outAdapter: outAdapter,
			channel:    channel,
			groupItem:  item,
			usedKey:    usedKey,
			keyOptions: keyOptions,
			keyIndex:   keyIndex,
			baseURL:    baseURL,
			keyRemark:  keyRemark,
		}, nil
	}

	return nil, nil
}
