package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// FinalOutboundRequest is the production-authoritative request shape after the
// provider transformer and every channel rewrite, but before network I/O.
type FinalOutboundRequest struct {
	Request  *httpclient.Request
	Artifact *requestartifact.Artifact
}

// BuildFinalOutboundRequest shares the exact transformer and middleware order
// used by relay attempts. It performs no network I/O and therefore powers both
// self-healing preview and isolated live diagnostics.
func BuildFinalOutboundRequest(
	ctx context.Context,
	channel *dbmodel.Channel,
	key dbmodel.ChannelKey,
	baseURL string,
	internalRequest *llm.Request,
) (*FinalOutboundRequest, error) {
	if channel == nil || internalRequest == nil {
		return nil, errors.New("channel and internal request are required")
	}
	if strings.TrimSpace(key.ChannelKey) == "" || strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("diagnostic outbound scope is incomplete")
	}
	attemptRequest := requestForOutboundPipeline(channel.Type, internalRequest)
	if attemptRequest == nil {
		return nil, errors.New("failed to clone diagnostic request")
	}
	// The ordinary relay pipeline rebuilds RawRequest through its inbound
	// middleware. This network-free builder has no inbound step, so preserve a
	// private copy of the caller-provided synthetic request for raw passthrough.
	// Self-healing never passes a production user's RawRequest here.
	if internalRequest.RawRequest != nil {
		rawCopy := *internalRequest.RawRequest
		rawCopy.Body = append([]byte(nil), internalRequest.RawRequest.Body...)
		if internalRequest.RawRequest.Headers != nil {
			rawCopy.Headers = internalRequest.RawRequest.Headers.Clone()
		}
		attemptRequest.RawRequest = &rawCopy
	}
	outbound, err := newChannelOutbound(channel, attemptRequest, baseURL, key)
	if err != nil {
		return nil, err
	}
	request, err := outbound.TransformRequest(ctx, attemptRequest)
	if err != nil {
		return nil, fmt.Errorf("transform diagnostic outbound request: %w", err)
	}
	metrics := &RelayMetrics{ActualModel: attemptRequest.Model}
	attempt := &relayAttempt{
		relayRun:   &relayRun{internalRequest: attemptRequest, metrics: metrics},
		outAdapter: outbound, channel: channel, usedKey: key, baseURL: baseURL,
	}
	if request.Headers == nil {
		request.Headers = make(map[string][]string)
	}
	attempt.applyChannelRequestOptions(request)
	if attempt.needsCrossFormatCleaning() {
		if err := attempt.stripCodexFieldsFromRequestBody(request); err != nil {
			return nil, fmt.Errorf("clean cross-format diagnostic request: %w", err)
		}
	}
	if err := attempt.applyRelayAttachmentCompatibility(request); err != nil {
		return nil, fmt.Errorf("preserve diagnostic outbound attachments: %w", err)
	}
	rewrite := requestartifact.RewriteSummary{}
	if summary := metrics.OutboundRequestSummary; summary != nil {
		rewrite = requestartifact.RewriteSummary{
			RawPassthrough: summary.RawPassthrough, ParamOverrideApplied: summary.ParamOverrideApplied,
			JSONRewriteApplied: summary.JSONRewriteApplied, HeaderRewriteApplied: summary.HeaderRewriteApplied,
			RequestRewriteApplied: summary.RequestRewriteApplied,
		}
	}
	return &FinalOutboundRequest{
		Request:  request,
		Artifact: requestartifact.Build(request, string(channel.Type), attemptRequest.Model, rewrite),
	}, nil
}
