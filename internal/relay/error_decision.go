package relay

import "github.com/bestruirui/octopus/internal/relay/errorclass"

// ErrorDecision is the relay-facing result of classifying an upstream failure.
// It keeps retry decisions next to classification so relay attempt code does
// not spread errorclass-specific checks across multiple branches.
type ErrorDecision struct {
	Classification errorclass.Classification
	RetryNextKey   bool
}

func decideRelayError(statusCode int, responseBody []byte, err error) ErrorDecision {
	classification := errorclass.Classify(statusCode, responseBody)
	if isFirstTokenTimeoutError(err) {
		classification = errorclass.Classification{
			Level:  errorclass.ErrorLevelChannel,
			Reason: "first token timeout",
		}
	}
	return ErrorDecision{
		Classification: classification,
		RetryNextKey:   classification.Level == errorclass.ErrorLevelKey,
	}
}
