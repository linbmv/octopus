package relay

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

func (ra *relayAttempt) recordSuccessfulChannelBaseline(ctx context.Context, statusCode int, headers http.Header) {
	if ra == nil || ra.channel == nil || ra.metrics == nil || ra.metrics.OutboundRequestArtifact == nil {
		return
	}
	settings := conf.Current().SelfHealing
	if !settings.Enabled || !settings.CaptureSuccessBaselines || !ra.channel.SelfHealingEnabled {
		return
	}
	now := time.Now().UTC()
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	contentType := ""
	if headers != nil {
		contentType = strings.TrimSpace(headers.Get("Content-Type"))
	}
	if contentType == "" {
		contentType = ra.metrics.OutboundRequestArtifact.Body.ContentType
	}
	baseline := &model.ChannelBaseline{
		ChannelID:           ra.channel.ID,
		ChannelKeyID:        ra.usedKey.ID,
		Model:               ra.metrics.ActualModel,
		WireProtocol:        ra.channel.Type,
		Endpoint:            ra.baseURL,
		EndpointFingerprint: model.CapabilityEndpointFingerprint(ra.baseURL),
		ScopeFingerprint:    model.CapabilityScopeFingerprint(ra.channel, ra.usedKey, ra.baseURL),
		RequestShape:        *ra.metrics.OutboundRequestArtifact,
		HTTPStatus:          statusCode,
		ContentType:         contentType,
		Source:              model.ChannelBaselineSourceRelaySuccess,
		CapturedAt:          now,
		ExpiresAt:           now.Add(time.Duration(settings.BaselineTTLSeconds) * time.Second),
	}
	// Baseline persistence is observability only and must never add latency or
	// failures to a successful relay response, so it runs off the request path.
	persistCtx := context.WithoutCancel(ctx)
	go func() {
		if err := op.ChannelBaselineCreate(persistCtx, baseline); err != nil {
			log.WithContext(persistCtx).Warnw("failed to persist successful channel baseline",
				"channel_id", baseline.ChannelID, "channel_key_id", baseline.ChannelKeyID,
				"model", baseline.Model, "error", err)
		}
	}()
}
