package resp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSuccessIncludesRequestID(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Set("request_id", "request-1")
	Success(context, map[string]string{"value": "ok"})

	var body ResponseStruct
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if response.Code != http.StatusOK || body.RequestID != "request-1" || body.Message != "success" {
		t.Fatalf("response = %#v, status=%d", body, response.Code)
	}
}

func TestErrorWithDetailsMapsCodes(t *testing.T) {
	statuses := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusTeapot,
	}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Set("request_id", "request-2")
			ErrorWithDetails(context, status, "message", map[string]interface{}{"field": "value"})
			if response.Code != status || !context.IsAborted() {
				t.Fatalf("status=%d aborted=%v", response.Code, context.IsAborted())
			}
			var body ResponseStruct
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if body.Error == nil || body.Error.Code == "" || body.RequestID != "request-2" {
				t.Fatalf("response = %#v", body)
			}
		})
	}
}

func TestErrorDelegatesToDetailedResponse(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	Error(context, http.StatusBadRequest, ErrBadRequest)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestErrorWithCodePreservesMachineReadableCode(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	ErrorWithCode(context, http.StatusForbidden, CodePasswordChangeRequired, ErrPasswordChange)

	var body ResponseStruct
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if response.Code != http.StatusForbidden || body.Error == nil || body.Error.Code != CodePasswordChangeRequired {
		t.Fatalf("response = %#v, status=%d", body, response.Code)
	}
}
