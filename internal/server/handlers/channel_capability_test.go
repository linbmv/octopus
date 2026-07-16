package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/capability"
	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestListChannelCapabilitiesReturnsFreshAccountScopeWithoutSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "capability-handler.db"), false); err != nil {
		t.Fatalf("init database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	channel := model.Channel{
		Name: "capability-handler", Type: llm.APIFormatOpenAIChatCompletion, Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test"}}, Model: "model-a",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "secret-account-key", Remark: "production"}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	now := time.Now().UTC()
	evidence := model.CapabilityEvidence{
		ChannelID: channel.ID, ChannelKeyID: channel.Keys[0].ID, Model: "model-a",
		WireProtocol: channel.Type, Capability: model.CapabilityTool, Status: model.CapabilitySupported,
		Endpoint: "https://provider.test", EndpointFingerprint: model.CapabilityEndpointFingerprint("https://provider.test"),
		ScopeFingerprint: model.CapabilityScopeFingerprint(&channel, channel.Keys[0], "https://provider.test"),
		Source:           "probe", ProbedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := op.CapabilityEvidenceUpsert(context.Background(), &evidence); err != nil {
		t.Fatalf("upsert evidence: %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel/1/capabilities", nil)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	listChannelCapabilities(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "secret-account-key") || strings.Contains(body, "scope_fingerprint") || strings.Contains(body, "endpoint_fingerprint") {
		t.Fatalf("capability response exposed an internal secret/fingerprint: %s", body)
	}
	var response struct {
		Data []struct {
			ChannelKeyID int    `json:"channel_key_id"`
			KeyRemark    string `json:"key_remark"`
			Fresh        bool   `json:"fresh"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ChannelKeyID != channel.Keys[0].ID || response.Data[0].KeyRemark != "production" || !response.Data[0].Fresh {
		t.Fatalf("capability account scope = %#v", response.Data)
	}
}

func TestProbeChannelCapabilitiesReportsDisabledWithoutPaidWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if _, err := capability.InstallDefault(conf.Default().CapabilityProbe); err != nil {
		t.Fatalf("install disabled worker: %v", err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/channel/1/capabilities/probe", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	probeChannelCapabilities(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "CAPABILITY_PROBE_DISABLED") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func stringInt(value int) string {
	return strconv.Itoa(value)
}
