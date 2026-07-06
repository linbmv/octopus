package relay

import (
	"errors"
	"fmt"
	"net/http"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
)

func (r *relayRun) run() {
	ctx := r.c.Request.Context()
	var lastErr error

	for {
		select {
		case <-ctx.Done():
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
				resp.Error(r.c, terminalErr.StatusCode(), terminalErr.Error())
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
			resp.Error(r.c, terminalErr.StatusCode(), terminalErr.Error())
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
	resp.Error(r.c, http.StatusBadGateway, lastErr.Error())
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
	childIter := newRelayIterator(*targetGroup, r.c.GetInt("api_key_id"), r.internalRequest, r.c.Request.Context())
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

	keyOptions := channel.AvailableKeysForAttempt(stickyKeyID)
	if len(keyOptions) == 0 {
		r.iter.Skip(channel.ID, 0, channel.Name, "no available key")
		return nil, nil
	}

	baseURL := selectRuntimeBaseURL(channel)
	for keyIndex, usedKey := range keyOptions {
		keyRemark := cleanKeyRemark(usedKey.Remark)
		if r.iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name, keyRemark) {
			continue
		}
		outAdapter, err := newOutbound(channel.Type, r.internalRequest, baseURL, usedKey.ChannelKey)
		if err != nil {
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
