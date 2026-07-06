package relay

import (
	"strings"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// ErrorDecision is the relay-facing result of classifying an upstream failure.
// It keeps retry decisions next to classification so relay attempt code does
// not spread errorclass-specific checks across multiple branches.
type ErrorDecision struct {
	Classification errorclass.Classification
	RetryNextKey   bool
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

func decideRelayError(statusCode int, responseBody []byte, err error) ErrorDecision {
	classification := errorclass.Classify(statusCode, responseBody)
	if isFirstTokenTimeoutError(err) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "first token timeout",
		}
	} else if isEmptyUpstreamResponseError(err) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "empty upstream response",
		}
	}
	return ErrorDecision{
		Classification: classification,
		RetryNextKey:   classification.Level == errorclass.ErrorLevelKey,
	}
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
