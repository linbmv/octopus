package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/gin-gonic/gin"
)

func TestRelayLogResponseSerializesSnowflakeIDAsString(t *testing.T) {
	const id = int64(9_007_199_254_740_993)
	data, err := json.Marshal(relayLogResponse(model.RelayLog{ID: id, Time: 123}))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var payload struct {
		ID   string `json:"id"`
		Time int64  `json:"time"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", err, data)
	}
	if payload.ID != "9007199254740993" || payload.Time != 123 {
		t.Fatalf("payload = %+v, want exact string ID and preserved fields", payload)
	}
}

func TestListLogRejectsInvalidCursorAndPartialTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []string{
		"/api/v1/log/list?cursor=not-an-id",
		"/api/v1/log/list?cursor=-1",
		"/api/v1/log/list?cursor=0&start_time=1",
	} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
			listLog(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMalformedStreamResumeDoesNotConsumeOneTimeToken(t *testing.T) {
	token, err := op.RelayLogStreamTokenCreate()
	if err != nil {
		t.Fatalf("RelayLogStreamTokenCreate() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/log/stream?token="+token+"&after=bad", nil)
	streamLog(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !op.RelayLogStreamTokenConsume(token) {
		t.Fatal("invalid resume cursor consumed the one-time stream token")
	}
}

func TestWriteRelayLogSSEIncludesExactEventID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	entry := model.RelayLog{ID: 9_007_199_254_740_993, Time: 123}
	if err := writeRelayLogSSE(ctx, entry); err != nil {
		t.Fatalf("writeRelayLogSSE() error = %v", err)
	}
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "id: 9007199254740993\n") || !strings.Contains(body, `"id":"9007199254740993"`) {
		t.Fatalf("SSE body did not preserve exact ID: %q", body)
	}
}
