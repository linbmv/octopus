package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/selfheal"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func initSelfHealingHandlerDB(t *testing.T, selfHealingEnabled bool) model.Channel {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "self-healing-handler.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{
		Name: "self-healing-handler", Type: llm.APIFormatOpenAIResponse, Enabled: true,
		SelfHealingEnabled: selfHealingEnabled, BaseUrls: []model.BaseUrl{{URL: "https://provider.test/v1"}},
		Model: "model-a", Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "handler-secret"}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatal(err)
	}
	return channel
}

func selfHealingRequest(t *testing.T, method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestPreviewChannelSelfHealingDoesNotUseNetwork(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	config := conf.Default().SelfHealing
	config.Enabled = true
	config.Diagnostic.MaxVariants = 2
	worker, err := selfheal.InstallDefault(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()
	c, recorder := selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/preview", `{"root_cause":"protocol_drift","max_variants":2}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	previewChannelSelfHealing(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "handler-secret") || strings.Contains(recorder.Body.String(), "authorization") {
		t.Fatalf("preview exposed credential material: %s", recorder.Body.String())
	}
}

func TestCreateChannelSelfHealingMapsGlobalAndChannelGates(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	config := conf.Default().SelfHealing
	config.Enabled = false
	worker, err := selfheal.InstallDefault(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()
	c, recorder := selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics", `{"root_cause":"protocol_drift"}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	createChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "SELF_HEALING_DISABLED") {
		t.Fatalf("global gate response = %d %s", recorder.Code, recorder.Body.String())
	}
	config.Enabled = true
	worker, err = selfheal.InstallDefault(config)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, SelfHealingEnabled: &disabled}, context.Background()); err != nil {
		t.Fatal(err)
	}
	c, recorder = selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics", `{"root_cause":"protocol_drift"}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	createChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "CHANNEL_SELF_HEALING_DISABLED") {
		t.Fatalf("channel gate response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func createSafeHandlerSession(t *testing.T, channel model.Channel, sessionStatus model.DiagnosticSessionStatus) (*model.DiagnosticSession, *model.ChannelPatch) {
	t.Helper()
	key := channel.Keys[0]
	now := time.Now().UTC()
	session := &model.DiagnosticSession{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: channel.Model, WireProtocol: channel.Type,
		Endpoint: channel.BaseUrls[0].URL, EndpointFingerprint: model.CapabilityEndpointFingerprint(channel.BaseUrls[0].URL),
		ScopeFingerprint: "scope-secret-should-not-leak", ConfigVersion: channel.ConfigVersion,
		Mode: model.DiagnosticModeLive, Trigger: model.DiagnosticTriggerManual, Status: model.DiagnosticSessionQueued,
		RootCause: model.RootCauseProtocolDrift, MaxAttempts: 2, Deadline: now.Add(time.Minute),
	}
	if err := op.DiagnosticSessionCreate(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	before := model.NewChannelPatchSnapshot(&channel)
	patch := &model.ChannelPatch{
		ChannelID: channel.ID, DiagnosticSessionID: session.ID, ExpectedScopeFingerprint: session.ScopeFingerprint,
		BaseChannelVersion: channel.ConfigVersion, Confidence: model.PatchConfidenceMedium,
		Changes:        []model.ChannelPatchChange{{Field: "user_agent", Before: json.RawMessage(`"authorization-secret"`), After: json.RawMessage(`"safe-agent"`), EvidenceVariantIDs: []string{"ua"}}},
		BeforeSnapshot: before, AfterSnapshot: before, VerificationModel: session.Model,
		VerificationEndpointFingerprint: session.EndpointFingerprint, MaxLiveRequests: 1, Status: model.ChannelPatchPreviewed,
	}
	if err := op.ChannelPatchCreate(context.Background(), patch); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != model.DiagnosticSessionQueued {
		if err := op.DiagnosticSessionStart(context.Background(), session.ID, now); err != nil {
			t.Fatal(err)
		}
		if sessionStatus != model.DiagnosticSessionRunning {
			if err := op.DiagnosticSessionFinishUpdate(context.Background(), session.ID, op.DiagnosticSessionFinish{Status: sessionStatus, RootCause: model.RootCauseProtocolDrift}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return session, patch
}

func TestDiagnosticResponseRedactsScopeAndPatchValues(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	session, _ := createSafeHandlerSession(t, channel, model.DiagnosticSessionCompleted)
	c, recorder := selfHealingRequest(t, http.MethodGet, "/api/v1/channel/1/self-healing/diagnostics/diag", "")
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}, {Key: "diagnostic_id", Value: session.ID}}
	getChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("diagnostic status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"scope-secret-should-not-leak", "authorization-secret", "handler-secret", "raw prompt"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("diagnostic response exposed %q: %s", forbidden, body)
		}
	}
	if strings.Contains(body, "scope_fingerprint") || strings.Contains(body, "expected_scope_fingerprint") {
		t.Fatalf("diagnostic response exposed internal fingerprint field: %s", body)
	}
}

func TestApplyChannelSelfHealingMapsVersionConflictAndChannelScope(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	oldFactory := newSelfHealingPatchService
	config := conf.Default().SelfHealing
	config.Enabled = true
	newSelfHealingPatchService = func() *selfheal.PatchService { return selfheal.NewPatchService(config, nil) }
	t.Cleanup(func() { newSelfHealingPatchService = oldFactory })
	session, patch := createSafeHandlerSession(t, channel, model.DiagnosticSessionCompleted)
	manual := "manual-agent"
	if _, err := op.ChannelUpdate(&model.ChannelUpdateRequest{ID: channel.ID, UserAgent: &manual}, context.Background()); err != nil {
		t.Fatal(err)
	}
	c, recorder := selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics/"+session.ID+"/apply", `{"patch_id":"`+patch.ID+`"}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}, {Key: "diagnostic_id", Value: session.ID}}
	applyChannelSelfHealingPatch(c)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "SELF_HEALING_CONFLICT") {
		t.Fatalf("version conflict response = %d %s", recorder.Code, recorder.Body.String())
	}
	other := model.Channel{
		Name: "other-self-healing-channel", Type: llm.APIFormatOpenAIResponse, Enabled: true,
		SelfHealingEnabled: true, BaseUrls: []model.BaseUrl{{URL: "https://other.test/v1"}}, Model: "model-b",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "other-secret"}},
	}
	if err := op.ChannelCreate(&other, context.Background()); err != nil {
		t.Fatal(err)
	}
	c, recorder = selfHealingRequest(t, http.MethodPost, "/api/v1/channel/"+stringInt(other.ID)+"/self-healing/diagnostics/"+session.ID+"/apply", `{"patch_id":"`+patch.ID+`"}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(other.ID)}, {Key: "diagnostic_id", Value: session.ID}}
	applyChannelSelfHealingPatch(c)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-channel apply status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSelfHealingAttemptResponseUsesOnlyRedactedArtifact(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	session, _ := createSafeHandlerSession(t, channel, model.DiagnosticSessionRunning)
	attempt := &model.DiagnosticAttempt{SessionID: session.ID, VariantID: "baseline", Status: model.DiagnosticAttemptRunning,
		RequestShape: *requestartifact.Build(&httpclient.Request{Headers: map[string][]string{"Authorization": []string{"secret"}}, Body: []byte(`{"prompt":"raw prompt"}`)}, "openai/responses", channel.Model, requestartifact.RewriteSummary{}), RootCause: model.RootCauseUnknown}
	if err := op.DiagnosticAttemptCreate(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	// The artifact builder stores only a redacted header and structural body
	// shape; the handler must preserve that contract when returning evidence.
	c, recorder := selfHealingRequest(t, http.MethodGet, "/api/v1/channel/1/self-healing/diagnostics/diag", "")
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}, {Key: "diagnostic_id", Value: session.ID}}
	getChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "raw prompt") {
		t.Fatalf("attempt response leaked sensitive content: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCompareChannelSelfHealingDiffsGoldenSampleWithoutNetwork(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	config := conf.Default().SelfHealing
	config.Enabled = true
	worker, err := selfheal.InstallDefault(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()
	body := `{"mode":"compare","root_cause":"protocol_drift","golden_sample":{` +
		`"method":"POST","url":"https://provider.test/v1/responses",` +
		`"headers":{"User-Agent":["codex-tui/0.144.6"],"originator":["codex-tui"]},` +
		`"body":{"model":"model-a","input":"hi","instructions":"x"}}}`
	c, recorder := selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics", body)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	createChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("compare status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := recorder.Body.String()
	if !strings.Contains(response, "header_diff") || !strings.Contains(response, "originator") {
		t.Fatalf("compare response missing header diff evidence: %s", response)
	}
	if strings.Contains(response, "handler-secret") || strings.Contains(strings.ToLower(response), "authorization") {
		t.Fatalf("compare response exposed credentials: %s", response)
	}
}

func TestCompareChannelSelfHealingRejectsSecretsAndMissingSample(t *testing.T) {
	channel := initSelfHealingHandlerDB(t, true)
	config := conf.Default().SelfHealing
	config.Enabled = true
	worker, err := selfheal.InstallDefault(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()

	c, recorder := selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics", `{"mode":"compare","root_cause":"protocol_drift"}`)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	createChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "SELF_HEALING_SAMPLE_REQUIRED") {
		t.Fatalf("missing sample response = %d %s", recorder.Code, recorder.Body.String())
	}

	secretBody := `{"mode":"compare","root_cause":"protocol_drift","golden_sample":{` +
		`"method":"POST","url":"https://provider.test/v1/responses",` +
		`"headers":{"Authorization":["Bearer sk-secret"]},"body":{"model":"m"}}}`
	c, recorder = selfHealingRequest(t, http.MethodPost, "/api/v1/channel/1/self-healing/diagnostics", secretBody)
	c.Params = gin.Params{{Key: "id", Value: stringInt(channel.ID)}}
	createChannelSelfHealingDiagnostic(c)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "SELF_HEALING_SAMPLE_SECRET") {
		t.Fatalf("secret sample response = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sk-secret") {
		t.Fatalf("secret sample value leaked into error response: %s", recorder.Body.String())
	}
}
