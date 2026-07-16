package handlers

import (
	"net/http"
	"testing"
)

func TestGetStatsErrorLevelsRejectsInvalidBoundaries(t *testing.T) {
	for _, path := range []string{
		"/stats/error-levels?window_hours=0",
		"/stats/error-levels?window_hours=169",
		"/stats/error-levels?window_hours=not-a-number",
		"/stats/error-levels?channel_id=-1",
		"/stats/error-levels?channel_id=not-a-number",
	} {
		response := invokeHandler(http.MethodGet, path, "", getStatsErrorLevels)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d; body=%s", path, response.Code, http.StatusBadRequest, response.Body.String())
		}
	}
}
