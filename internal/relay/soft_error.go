package relay

import (
	"net/http"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// These helpers remain as thin compatibility/test boundaries. All HTTP, JSON,
// plain-text, and SSE response rules live in errorclass.ClassifyResponse.
func isSoftError(statusCode int, body []byte, contentType string) bool {
	if statusCode != http.StatusOK || len(body) == 0 {
		return false
	}
	return errorclass.ClassifyResponse(statusCode, nil, body, contentType).Level != errorclass.ErrorLevelNone
}

func isJSONSoftError(body []byte) bool {
	return isSoftError(http.StatusOK, body, "application/json")
}

func isSSESoftError(body []byte) bool {
	return isSoftError(http.StatusOK, body, "text/event-stream")
}

func isPlainTextSoftError(body []byte) bool {
	return isSoftError(http.StatusOK, body, "text/plain")
}
