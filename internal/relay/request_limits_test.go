package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestParseRequestReturnsStable413ForOversizedJSONAndImageBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setRelayLimitConfig(t, 4, 6)

	for _, test := range []struct {
		name      string
		format    llm.APIFormat
		body      []byte
		wantLimit int64
	}{
		{name: "JSON", format: llm.APIFormatOpenAIChatCompletion, body: []byte("12345"), wantLimit: 4},
		{name: "image", format: llm.APIFormatOpenAIImageEdit, body: []byte("1234567"), wantLimit: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(test.body))
			ctx.Request.ContentLength = -1 // exercise bounded reads, not only declared-length prechecks
			_, err := parseRequest(ctx, test.format, newInbound(test.format))
			if !errors.Is(err, errRelayRequestBodyTooLarge) {
				t.Fatalf("parseRequest() error = %v, want body limit", err)
			}
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
			}
			assertRelayErrorCode(t, response.Body.Bytes(), "REQUEST_BODY_TOO_LARGE")
		})
	}
}

func TestParseRequestRejectsCompressedBodyWithoutReadingIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &relayTrackedBody{Reader: bytes.NewReader([]byte("compressed"))}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	ctx.Request.Header.Set("Content-Encoding", "gzip")

	_, err := parseRequest(ctx, llm.APIFormatOpenAIChatCompletion, newInbound(llm.APIFormatOpenAIChatCompletion))
	if !errors.Is(err, errRelayContentEncodingUnsupported) {
		t.Fatalf("parseRequest() error = %v, want content-encoding sentinel", err)
	}
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", response.Code)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("compressed body was read %d times", body.reads.Load())
	}
	assertRelayErrorCode(t, response.Body.Bytes(), "REQUEST_CONTENT_ENCODING_UNSUPPORTED")
}

func setRelayLimitConfig(t *testing.T, jsonBytes, imageBytes int64) {
	t.Helper()
	old := conf.Current()
	config := old
	config.Relay.MaxJSONRequestBytes = jsonBytes
	config.Relay.MaxImageRequestBytes = imageBytes
	if err := conf.Set(config); err != nil {
		t.Fatalf("conf.Set() error = %v", err)
	}
	t.Cleanup(func() {
		if err := conf.Set(old); err != nil {
			t.Errorf("restore config: %v", err)
		}
	})
}

func assertRelayErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, body)
	}
	if response.Error.Code != want {
		t.Fatalf("error code = %q, want %q", response.Error.Code, want)
	}
}
