package op

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestRelayLogMetadataPolicyDropsBodiesButKeepsDiagnostics(t *testing.T) {
	relayLog := model.RelayLog{
		RequestContent:  `{"messages":[{"content":"private prompt"}]}`,
		ResponseContent: `{"content":"private response"}`,
		OutputTokens:    10,
		ReasoningTokens: 6,
		Error:           "upstream timeout",
		Attempts: []model.ChannelAttempt{{
			Status: model.AttemptFailed,
			Msg:    "connection refused",
		}},
	}

	got := applyRelayLogContentPolicy(relayLog, model.RelayLogContentModeMetadata)
	if got.RequestContent != "" || got.ResponseContent != "" {
		t.Fatalf("metadata mode persisted bodies: request=%q response=%q", got.RequestContent, got.ResponseContent)
	}
	if got.Error != relayLog.Error || len(got.Attempts) != 1 || got.Attempts[0].Msg != relayLog.Attempts[0].Msg {
		t.Fatalf("metadata mode lost diagnostics: %#v", got)
	}
	if got.OutputTokens != 10 || got.ReasoningTokens != 6 {
		t.Fatalf("metadata mode lost token metadata: %#v", got)
	}
}

func TestRelayLogFullPolicyRedactsCredentialFields(t *testing.T) {
	relayLog := model.RelayLog{
		RequestContent: `{
			"messages":[{"content":"keep this prompt"}],
			"authorization":"Bearer request-secret",
			"headers":{"X-Api-Key":"header-secret","X-Goog-Api-Key":"google-secret","X-Trace":"keep-trace"},
			"metadata":{"client_secret":"client-secret","key":"generic-key-secret","signature":"signature-secret"},
			"endpoint":"https://user:password@example.com/v1?key=query-secret"
		}`,
		ResponseContent: "data: {\"result\":\"keep this response\",\"access_token\":\"response-secret\"}\n\n",
		Error:           "POST https://admin:pass@example.com/v1?signature=error-secret failed; Authorization: Bearer error-bearer",
		Attempts: []model.ChannelAttempt{{
			Status: model.AttemptFailed,
			Msg:    "request failed; Cookie: session=attempt-secret; secondary=cookie-tail-secret\nGET https://user:url-secret@%zz/v1?key=url-query-secret",
		}},
	}

	got := applyRelayLogContentPolicy(relayLog, model.RelayLogContentModeFull)
	combined := got.RequestContent + got.ResponseContent + got.Error + got.Attempts[0].Msg
	for _, secret := range []string{
		"request-secret", "header-secret", "google-secret", "client-secret", "query-secret",
		"response-secret", "generic-key-secret", "signature-secret", "password", "error-secret", "error-bearer", "attempt-secret",
		"cookie-tail-secret", "url-secret", "url-query-secret",
	} {
		if strings.Contains(combined, secret) {
			t.Errorf("full mode retained secret %q in %q", secret, combined)
		}
	}
	for _, diagnostic := range []string{"keep this prompt", "keep-trace", "keep this response", "example.com", relayLogRedactedValue} {
		if !strings.Contains(combined, diagnostic) {
			t.Errorf("full mode lost expected diagnostic %q in %q", diagnostic, combined)
		}
	}
}

func TestRelayLogFullPolicyRedactsMalformedAndTruncatedJSONBeforeLimitingSize(t *testing.T) {
	malformed := `{"api_key":"malformed-secret,"content":"keep"}`
	got := applyRelayLogContentPolicy(model.RelayLog{RequestContent: malformed}, model.RelayLogContentModeFull)
	if strings.Contains(got.RequestContent, "malformed-secret") {
		t.Fatalf("malformed JSON retained API key: %q", got.RequestContent)
	}

	large := `{"api_key":"large-secret","content":"` + strings.Repeat("x", conf.MaxRelayLogContentBytes+100) + `"}`
	got = applyRelayLogContentPolicy(model.RelayLog{RequestContent: large}, model.RelayLogContentModeFull)
	if strings.Contains(got.RequestContent, "large-secret") {
		t.Fatalf("large JSON retained API key: %q", got.RequestContent[:min(len(got.RequestContent), 200)])
	}
	if !strings.HasSuffix(got.RequestContent, "\n[truncated]") {
		t.Fatal("large content was not bounded after redaction")
	}
	if len(got.RequestContent) > conf.MaxRelayLogContentBytes {
		t.Fatalf("bounded content length = %d, max = %d", len(got.RequestContent), conf.MaxRelayLogContentBytes)
	}
}

func TestRelayLogMetadataPolicyBoundsDiagnosticsAtUTF8Boundary(t *testing.T) {
	oversized := strings.Repeat("界", conf.MaxRelayLogContentBytes)
	got := applyRelayLogContentPolicy(model.RelayLog{
		Error: oversized,
		Attempts: []model.ChannelAttempt{{
			Msg: oversized,
		}},
	}, model.RelayLogContentModeMetadata)
	for field, value := range map[string]string{"error": got.Error, "attempt": got.Attempts[0].Msg} {
		if len(value) > conf.MaxRelayLogContentBytes {
			t.Fatalf("%s length = %d, max = %d", field, len(value), conf.MaxRelayLogContentBytes)
		}
		if !utf8.ValidString(value) {
			t.Fatalf("%s was truncated inside a UTF-8 rune", field)
		}
	}
}

func TestRelayLogPolicyNormalizesInvalidUTF8(t *testing.T) {
	got := applyRelayLogContentPolicy(model.RelayLog{Error: "upstream \xff error"}, model.RelayLogContentModeMetadata)
	if !utf8.ValidString(got.Error) {
		t.Fatalf("sanitized error is not valid UTF-8: %q", got.Error)
	}
}

func TestRelayLogServiceDisabledModeProducesNoRelayLog(t *testing.T) {
	restoreRelayLogSettings := setRelayLogSettingsForTest(t, model.RelayLogContentModeDisabled, true)
	defer restoreRelayLogSettings()

	service := NewRelayLogService()
	if err := service.Add(context.Background(), model.RelayLog{
		RequestContent:  "private request",
		ResponseContent: "private response",
		Error:           "diagnostic",
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	service.cacheMu.Lock()
	cacheLen := len(service.cache)
	service.cacheMu.Unlock()
	if cacheLen != 0 || len(service.notifyQueue) != 0 {
		t.Fatalf("disabled mode produced relay log: cache=%d notifications=%d", cacheLen, len(service.notifyQueue))
	}
}

func TestRelayLogServiceFailsClosedWithoutValidContentPolicy(t *testing.T) {
	saved := settingCache.GetAll()
	defer func() {
		settingCache.Clear()
		for key, value := range saved {
			settingCache.Set(key, value)
		}
	}()

	invalidMode := "full-ish"
	for _, test := range []struct {
		name  string
		value *string
	}{
		{name: "missing"},
		{name: "invalid", value: &invalidMode},
	} {
		t.Run(test.name, func(t *testing.T) {
			settingCache.Clear()
			settingCache.Set(model.SettingKeyRelayLogKeepEnabled, "true")
			if test.value != nil {
				settingCache.Set(model.SettingKeyRelayLogContentMode, *test.value)
			}
			service := NewRelayLogService()
			if err := service.Add(context.Background(), model.RelayLog{RequestContent: "private"}); err == nil {
				t.Fatal("Add() error = nil, want fail-closed policy error")
			}
			service.cacheMu.Lock()
			cacheLen := len(service.cache)
			service.cacheMu.Unlock()
			if cacheLen != 0 || len(service.notifyQueue) != 0 {
				t.Fatalf("invalid policy produced relay log: cache=%d notifications=%d", cacheLen, len(service.notifyQueue))
			}
		})
	}
}

func TestRelayLogFullModePersistsOnlyRedactedCredentials(t *testing.T) {
	setupRelayLogPersistenceTest(t)
	settingCache.Set(model.SettingKeyRelayLogContentMode, string(model.RelayLogContentModeFull))

	service := NewRelayLogService()
	if err := service.Add(context.Background(), model.RelayLog{
		Time:             1,
		RequestModelName: "test-model",
		OutputTokens:     10,
		ReasoningTokens:  6,
		RequestContent:   `{"messages":[{"content":"keep prompt"}],"headers":{"X-Goog-Api-Key":"db-request-secret"}}`,
		ResponseContent:  "X-Api-Key: db-response-secret\nresult: keep response",
		Error:            "GET https://user:db-password@example.com/v1?key=db-query-secret failed",
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := service.flushToDB(context.Background()); err != nil {
		t.Fatalf("flushToDB() error = %v", err)
	}

	var persisted model.RelayLog
	if err := db.GetDB().First(&persisted).Error; err != nil {
		t.Fatalf("read persisted relay log: %v", err)
	}
	if persisted.OutputTokens != 10 || persisted.ReasoningTokens != 6 {
		t.Fatalf("database lost token metadata: %#v", persisted)
	}
	combined := persisted.RequestContent + persisted.ResponseContent + persisted.Error
	for _, secret := range []string{"db-request-secret", "db-response-secret", "db-password", "db-query-secret"} {
		if strings.Contains(combined, secret) {
			t.Errorf("database retained credential %q in %q", secret, combined)
		}
	}
	for _, diagnostic := range []string{"keep prompt", "keep response", "example.com"} {
		if !strings.Contains(combined, diagnostic) {
			t.Errorf("database lost expected diagnostic %q in %q", diagnostic, combined)
		}
	}
}

func TestRelayLogContentPolicyConcurrentHotReload(t *testing.T) {
	restoreRelayLogSettings := setRelayLogSettingsForTest(t, model.RelayLogContentModeMetadata, false)
	defer restoreRelayLogSettings()

	service := NewRelayLogService()
	const total = 300
	var wg sync.WaitGroup
	wg.Add(total + 1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			mode := model.RelayLogContentModeMetadata
			if i%2 == 0 {
				mode = model.RelayLogContentModeFull
			}
			settingCache.Set(model.SettingKeyRelayLogContentMode, string(mode))
		}
	}()
	for i := 0; i < total; i++ {
		go func(id int) {
			defer wg.Done()
			err := service.Add(context.Background(), model.RelayLog{
				Time:             int64(id),
				RequestModelName: fmt.Sprintf("model-%d", id),
				RequestContent:   `{"api_key":"never-store-this","messages":[{"content":"prompt"}]}`,
			})
			if err != nil {
				t.Errorf("Add(%d) error = %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	service.cacheMu.Lock()
	logs := append([]model.RelayLog(nil), service.cache...)
	service.cacheMu.Unlock()
	if len(logs) == 0 || len(logs) >= relayLogCacheHardLimit {
		t.Fatalf("unexpected bounded cache length after concurrent adds: %d", len(logs))
	}
	for _, relayLog := range logs {
		if strings.Contains(relayLog.RequestContent, "never-store-this") {
			t.Fatalf("hot reload race retained API key: %#v", relayLog)
		}
	}
}

func setRelayLogSettingsForTest(t *testing.T, mode model.RelayLogContentMode, keepEnabled bool) func() {
	t.Helper()
	saved := settingCache.GetAll()
	settingCache.Clear()
	settingCache.Set(model.SettingKeyRelayLogContentMode, string(mode))
	settingCache.Set(model.SettingKeyRelayLogKeepEnabled, fmt.Sprintf("%t", keepEnabled))
	settingCache.Set(model.SettingKeyRelayLogKeepPeriod, "0")
	return func() {
		settingCache.Clear()
		for key, value := range saved {
			settingCache.Set(key, value)
		}
	}
}
