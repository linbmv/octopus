package relay

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/utils/bodylimit"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

type ErrorAction string

const (
	ErrorActionNone         ErrorAction = "none"
	ErrorActionReturnClient ErrorAction = "return_client"
	ErrorActionRetryKey     ErrorAction = "retry_key"
	ErrorActionRetryChannel ErrorAction = "retry_channel"
)

type CompactCompatibilityAction string

const (
	CompactCompatibilityNone             CompactCompatibilityAction = "none"
	CompactCompatibilityMarkIncompatible CompactCompatibilityAction = "mark_incompatible"
)

// ErrorDecision is the relay-facing result of classifying an upstream failure.
// It keeps retry decisions next to classification so relay attempt code does
// not spread errorclass-specific checks across multiple branches.
type ErrorDecision struct {
	Classification   errorclass.Classification
	Action           ErrorAction
	ClientStatusCode int
	CompactAction    CompactCompatibilityAction

	// RetryNextKey is kept for callers/tests compiled against the first
	// ErrorDecision shape. New code should branch on Action.
	RetryNextKey bool
}

type errorDecisionOptions struct {
	OfficialCompactEndpoint bool
}

type terminalRelayError struct {
	err        error
	statusCode int
}

func (e *terminalRelayError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *terminalRelayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *terminalRelayError) StatusCode() int {
	if e == nil || e.statusCode == 0 {
		return 502
	}
	return e.statusCode
}

func newTerminalRelayError(statusCode int, err error) error {
	return &terminalRelayError{statusCode: statusCode, err: err}
}

func decideRelayError(statusCode int, headers http.Header, responseBody []byte, err error) ErrorDecision {
	return decideRelayErrorWithOptions(statusCode, headers, responseBody, err, errorDecisionOptions{})
}

func decideRelayErrorWithOptions(statusCode int, headers http.Header, responseBody []byte, err error, options errorDecisionOptions) ErrorDecision {
	contentType := ""
	if headers != nil {
		contentType = headers.Get("Content-Type")
	}
	classification := errorclass.ClassifyResponse(statusCode, headers, responseBody, contentType)
	if isFirstTokenTimeoutError(err) {
		reason := "first token timeout"
		var timeoutErr *firstTokenTimeoutError
		if errors.As(err, &timeoutErr) && timeoutErr.config.Source == firstTokenTimeoutNonStreamAttempt {
			reason = "non-stream attempt timeout"
		}
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: reason,
		}
	} else if errors.Is(err, errNonStreamRequestTimeout) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "non-streaming request timeout",
		}
	} else if errors.Is(err, errStreamIdleTimeout) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "stream idle timeout",
		}
	} else if errors.Is(err, bodylimit.ErrTooLarge) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "upstream response body too large",
		}
	} else if isEmptyUpstreamResponseError(err) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "empty upstream response",
		}
	} else if err != nil && classification.Level == errorclass.ErrorLevelNone {
		// Network failures, malformed/soft 2xx responses, and other failures
		// without an HTTP error status are properties of the selected upstream,
		// not of a client key. Never persist a failed attempt with level=none.
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "upstream transport or response failure",
		}
	}
	decision := decisionForClassification(statusCode, classification)
	if options.OfficialCompactEndpoint && isCompactEndpointUnsupported(statusCode, responseBody, err) {
		decision.Classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "official compact endpoint is incompatible",
		}
		decision.Action = ErrorActionRetryChannel
		decision.ClientStatusCode = 0
		decision.CompactAction = CompactCompatibilityMarkIncompatible
		decision.RetryNextKey = false
	}
	return decision
}

func decisionForClassification(statusCode int, classification errorclass.Classification) ErrorDecision {
	decision := ErrorDecision{
		Classification: classification,
		Action:         ErrorActionNone,
		CompactAction:  CompactCompatibilityNone,
	}
	switch classification.Level {
	case errorclass.ErrorLevelClient:
		decision.Action = ErrorActionReturnClient
		decision.ClientStatusCode = statusCode
		if decision.ClientStatusCode < 400 || decision.ClientStatusCode > 499 {
			decision.ClientStatusCode = http.StatusBadRequest
		}
	case errorclass.ErrorLevelKey:
		decision.Action = ErrorActionRetryKey
		decision.RetryNextKey = true
	case errorclass.ErrorLevelChannel:
		decision.Action = ErrorActionRetryChannel
	}
	return decision
}

func (ra *relayAttempt) decideError(statusCode int, headers http.Header, responseBody []byte, err error) ErrorDecision {
	officialCompact := ra != nil && ra.relayRun != nil && ra.channel != nil &&
		isCompactOpenAIChannel(ra.channel.Type, ra.internalRequest)
	return decideRelayErrorWithOptions(statusCode, headers, responseBody, err, errorDecisionOptions{
		OfficialCompactEndpoint: officialCompact,
	})
}

// isEndpointUnsupportedError is retained as a compatibility wrapper for the
// focused endpoint tests. Compact routing uses isCompactEndpointUnsupported so
// it can also inspect the response body and avoid model_not_found false marks.
func isEndpointUnsupportedError(err error) bool {
	return isCompactEndpointUnsupported(0, nil, err)
}

func isCompactEndpointUnsupported(statusCode int, responseBody []byte, err error) bool {
	if err == nil && statusCode == 0 {
		return false
	}
	text := boundedErrorDecisionText(responseBody, err)
	if containsModelNotFoundMarker(text) {
		return false
	}
	for _, marker := range []string{
		"invalid url",
		"no such endpoint",
		"unknown endpoint",
		"route not found",
		"cannot post",
		"unsupported endpoint",
		"endpoint not found",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if statusCode == 0 {
		statusCode = upstreamErrorStatus(err)
	}
	switch statusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func boundedErrorDecisionText(responseBody []byte, err error) string {
	const maxDecisionTextBytes = 8192
	if len(responseBody) > maxDecisionTextBytes {
		responseBody = responseBody[:maxDecisionTextBytes]
	}
	var b strings.Builder
	b.Grow(len(responseBody) + 128)
	b.Write(responseBody)
	if err != nil {
		b.WriteByte(' ')
		message := err.Error()
		if len(message) > maxDecisionTextBytes {
			message = message[:maxDecisionTextBytes]
		}
		b.WriteString(message)
	}
	return strings.ToLower(b.String())
}

func containsModelNotFoundMarker(text string) bool {
	return strings.Contains(text, "model_not_found") ||
		strings.Contains(text, "model not found") ||
		(strings.Contains(text, "model") && strings.Contains(text, "does not exist"))
}

func upstreamErrorStatus(err error) int {
	var responseErr *llm.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		return responseErr.StatusCode
	}
	var upstreamErr *httpclient.Error
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return upstreamErr.StatusCode
	}
	return 0
}

func isEmptyUpstreamResponseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "response body is empty") ||
		strings.Contains(msg, "empty response detected") ||
		strings.Contains(msg, "empty pipeline result") ||
		strings.Contains(msg, "empty pipeline response") ||
		strings.Contains(msg, "empty pipeline stream")
}
