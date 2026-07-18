package relay

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestErrorDecisionUsesExplicitActions(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		headers      http.Header
		body         string
		err          error
		wantAction   ErrorAction
		wantLevel    string
		wantClient   int
		wantRetryKey bool
	}{
		{
			name:       "deterministic upstream client error returns to client",
			status:     http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error"}}`,
			err:        errors.New("invalid request"),
			wantAction: ErrorActionReturnClient,
			wantLevel:  "client",
			wantClient: http.StatusBadRequest,
		},
		{
			name:       "upstream tool call state mismatch switches channel without health penalty",
			status:     http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"No tool call found for function call output with call_id call_IqfZxSEUdLKWipnlRTX4w5xm."}}`,
			err:        errors.New("bad request"),
			wantAction: ErrorActionRetryChannel,
			wantLevel:  "client",
			wantClient: http.StatusBadRequest,
		},
		{
			name:         "key error rotates key",
			status:       http.StatusTooManyRequests,
			err:          errors.New("rate limited"),
			wantAction:   ErrorActionRetryKey,
			wantLevel:    "key",
			wantRetryKey: true,
		},
		{
			name:       "channel error switches channel",
			status:     http.StatusBadGateway,
			err:        errors.New("bad gateway"),
			wantAction: ErrorActionRetryChannel,
			wantLevel:  "channel",
		},
		{
			name:       "HTTP 200 soft overload switches channel",
			status:     http.StatusOK,
			headers:    http.Header{"Content-Type": {"application/json"}},
			body:       `{"type":"error","error":{"type":"overloaded_error"}}`,
			err:        errors.New("soft error"),
			wantAction: ErrorActionRetryChannel,
			wantLevel:  "channel",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := decideRelayError(test.status, test.headers, []byte(test.body), test.err)
			if decision.Action != test.wantAction || decision.Classification.Level.String() != test.wantLevel {
				t.Fatalf("decision = %#v, want action=%s level=%s", decision, test.wantAction, test.wantLevel)
			}
			if decision.ClientStatusCode != test.wantClient {
				t.Fatalf("ClientStatusCode = %d, want %d", decision.ClientStatusCode, test.wantClient)
			}
			if decision.RetryNextKey != test.wantRetryKey {
				t.Fatalf("RetryNextKey = %v, want %v", decision.RetryNextKey, test.wantRetryKey)
			}
		})
	}
}

func TestClientErrorRetryPolicyUsesChannelProfile(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error"}}`)
	for _, test := range []struct {
		name    string
		profile dbmodel.ChannelPolicyProfile
		want    ErrorAction
	}{
		{name: "standard is conservative", profile: dbmodel.ChannelPolicyStandard, want: ErrorActionReturnClient},
		{name: "official is authoritative", profile: dbmodel.ChannelPolicyOfficial, want: ErrorActionReturnClient},
		{name: "trusted proxy may differ", profile: dbmodel.ChannelPolicyTrustedProxy, want: ErrorActionRetryChannel},
		{name: "untrusted proxy may misclassify", profile: dbmodel.ChannelPolicyUntrustedProxy, want: ErrorActionRetryChannel},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := decideRelayErrorWithOptions(http.StatusBadRequest, nil, body, errors.New("bad request"), errorDecisionOptions{
				PolicyProfile: test.profile,
			})
			if decision.Action != test.want || decision.ClientStatusCode != http.StatusBadRequest {
				t.Fatalf("decision = %+v, want action=%s status=400", decision, test.want)
			}
		})
	}
}

func TestCompactEndpointCompatibilityDecisionIsConservative(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		err         error
		official    bool
		wantAction  ErrorAction
		wantCompact CompactCompatibilityAction
	}{
		{name: "safe route 404", status: 404, body: `{"error":{"message":"no such endpoint"}}`, err: errors.New("not found"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityMarkIncompatible},
		{name: "method unsupported", status: 405, err: errors.New("method not allowed"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityMarkIncompatible},
		{name: "not implemented", status: 501, err: errors.New("not implemented"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityMarkIncompatible},
		{name: "explicit endpoint marker", status: 400, body: `{"error":"unsupported endpoint"}`, err: errors.New("bad request"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityMarkIncompatible},
		{name: "model not found switches channel", status: 404, body: `{"error":{"code":"model_not_found","message":"model not found"}}`, err: errors.New("not found"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityNone},
		{name: "rate limit is not incompatibility", status: 429, body: `{"error":"rate limit on /responses/compact"}`, err: errors.New("rate limited"), official: true, wantAction: ErrorActionRetryKey, wantCompact: CompactCompatibilityNone},
		{name: "generic server error is not incompatibility", status: 500, body: `{"error":"temporary failure"}`, err: errors.New("server error"), official: true, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityNone},
		{name: "ordinary 404 has no compact side effect", status: 404, body: `{"error":"route not found"}`, err: errors.New("not found"), official: false, wantAction: ErrorActionRetryChannel, wantCompact: CompactCompatibilityNone},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := decideRelayErrorWithOptions(test.status, nil, []byte(test.body), test.err, errorDecisionOptions{
				OfficialCompactEndpoint: test.official,
			})
			if decision.Action != test.wantAction || decision.CompactAction != test.wantCompact {
				t.Fatalf("decision = %#v, want action=%s compact=%s", decision, test.wantAction, test.wantCompact)
			}
		})
	}
}

func TestCompactIncompatibleDecisionPersistsWithoutManualDowngrade(t *testing.T) {
	request := &llm.Request{RequestType: llm.RequestTypeCompact}
	var persisted dbmodel.CompactStrategy
	var calls int
	ra := &relayAttempt{
		relayRun: &relayRun{internalRequest: request},
		channel:  &dbmodel.Channel{Type: llm.APIFormatOpenAIResponse},
		groupItem: dbmodel.GroupItem{
			ID:      11,
			GroupID: 22,
		},
		compactStrategyUpdater: func(itemID, groupID int, strategy dbmodel.CompactStrategy, updatedAt time.Time, ctx context.Context) error {
			calls++
			if itemID != 11 || groupID != 22 || updatedAt.IsZero() || ctx == nil {
				t.Fatalf("unexpected persistence call: item=%d group=%d updated=%v ctx=%v", itemID, groupID, updatedAt, ctx)
			}
			persisted = strategy
			return nil
		},
	}

	decision := ra.decideError(http.StatusNotFound, nil, []byte(`{"error":"no such endpoint"}`), errors.New("not found"))
	if decision.Action != ErrorActionRetryChannel || decision.CompactAction != CompactCompatibilityMarkIncompatible {
		t.Fatalf("decision = %#v", decision)
	}
	ra.applyCompactCompatibilityDecision(context.Background(), decision)
	if calls != 1 || persisted != dbmodel.CompactStrategyIncompatible || ra.groupItem.CompactStrategy != dbmodel.CompactStrategyIncompatible {
		t.Fatalf("calls=%d persisted=%q item=%q", calls, persisted, ra.groupItem.CompactStrategy)
	}

	modelDecision := ra.decideError(http.StatusNotFound, nil, []byte(`{"error":{"code":"model_not_found"}}`), errors.New("model not found"))
	ra.applyCompactCompatibilityDecision(context.Background(), modelDecision)
	if calls != 1 {
		t.Fatalf("model_not_found triggered incompatibility persistence; calls=%d", calls)
	}
}
