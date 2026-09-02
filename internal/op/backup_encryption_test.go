package op

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestConfigBackupEncryptionRoundTrip(t *testing.T) {
	dump := &model.ConfigDump{
		Version: configDumpVersion,
		Scope:   model.ConfigDumpScope,
		Channels: []model.ConfigChannel{{
			ID: 7, Name: "primary", Key: "secret-key", BaseURL: "https://example.test",
		}},
		Groups: []model.ConfigGroup{{ID: 9, Name: "main", Mode: model.GroupModeManual}},
	}
	encrypted, err := EncryptConfigDumpForTest(dump, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("encrypt config: %v", err)
	}
	decoded, err := DecodeConfigDump(encrypted, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if len(decoded.Channels) != 1 || decoded.Channels[0].Key != "secret-key" || len(decoded.Groups) != 1 {
		t.Fatalf("decoded config = %#v", decoded)
	}
	if bytes.Contains(encrypted, []byte("secret-key")) {
		t.Fatal("encrypted backup contains plaintext credential")
	}
}

func TestConfigBackupWireOmitsRuntimeStatistics(t *testing.T) {
	raw, err := json.Marshal(model.ConfigChannel{ID: 1, Name: "channel", Key: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("input_token")) || bytes.Contains(raw, []byte("request_success")) {
		t.Fatalf("config channel contains runtime statistics: %s", raw)
	}
	raw, err = json.Marshal(model.ConfigChannelModel{ID: 2, ChannelID: 1, Name: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("input_token")) || bytes.Contains(raw, []byte("request_success")) {
		t.Fatalf("config channel model contains runtime statistics: %s", raw)
	}
}

func TestConfigBackupRejectsPlaintextAndWrongPassword(t *testing.T) {
	dump := &model.ConfigDump{Version: configDumpVersion, Scope: model.ConfigDumpScope}
	encrypted, err := EncryptConfigDumpForTest(dump, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("encrypt config: %v", err)
	}
	if _, err := DecodeConfigDump([]byte(`{"version":4,"scope":"config"}`), []byte("correct horse battery staple")); !errors.Is(err, ErrDBBackupUnsupported) {
		t.Fatalf("plaintext error = %v, want unsupported backup", err)
	}
	if _, err := DecodeConfigDump(encrypted, []byte("wrong password")); !errors.Is(err, ErrDBBackupAuthentication) {
		t.Fatalf("wrong password error = %v, want authentication error", err)
	}
}

func TestDecodeConfigDumpConvertsEdgeV2AndExcludesOAuthAndHealth(t *testing.T) {
	raw := []byte(`{"version":2,"exported_at":"2026-01-02T03:04:05Z","include_logs":true,"include_stats":true,
"channels":[
  {"id":10,"name":"openai-main","type":"openai/chat_completions","enabled":true,
   "base_urls":[{"url":"https://one.example/v1","delay":0},{"url":"https://two.example/v1","delay":10}],
   "model":"gpt-4o","custom_model":"gpt-custom","auto_sync":true},
  {"id":11,"name":"codex","type":"openai/codex","base_urls":[{"url":"https://codex.example"}],"model":"codex-mini"}],
"channel_keys":[{"id":21,"channel_id":10,"enabled":true,"channel_key":"key-a"},{"id":22,"channel_id":10,"enabled":false,"channel_key":"key-b"}],
"groups":[{"id":30,"name":"all","enabled":true,"mode":3}],
"group_items":[{"id":40,"group_id":30,"type":"channel","channel_id":10,"model_name":"gpt-4o","priority":1},{"id":41,"group_id":30,"type":"channel","channel_id":11,"model_name":"codex-mini","priority":2}],
"settings":[{"key":"proxy_url","value":"http://proxy.example"},{"key":"smart_health_enabled","value":"true"}]}`)
	encrypted, err := EncryptDBBackup(raw, []byte("correct horse battery staple"), ConfigBackupMaxBytes)
	if err != nil {
		t.Fatalf("encrypt Edge dump: %v", err)
	}
	dump, err := DecodeConfigDump(encrypted, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("convert Edge dump: %v", err)
	}
	if len(dump.Channels) != 1 || dump.Channels[0].Name != "openai-main" {
		t.Fatalf("channels = %#v", dump.Channels)
	}
	if len(dump.Channels[0].BaseUrls) != 2 || len(dump.Channels[0].Keys) != 2 || dump.Channels[0].Key != "key-a" {
		t.Fatalf("multi-value channel config = %#v", dump.Channels[0])
	}
	if len(dump.ChannelModels) != 2 || len(dump.GroupItems) != 1 || dump.Groups[0].Mode != model.GroupModeFailover {
		t.Fatalf("converted models/groups = %#v %#v %#v", dump.ChannelModels, dump.Groups, dump.GroupItems)
	}
	if len(dump.Settings) != 1 || dump.Settings[0].Key != model.SettingKeyProxyURL {
		t.Fatalf("settings = %#v", dump.Settings)
	}
	joinedWarnings, _ := json.Marshal(dump.Warnings)
	if !bytes.Contains(joinedWarnings, []byte("Codex OAuth")) || !bytes.Contains(joinedWarnings, []byte("statistics")) || !bytes.Contains(joinedWarnings, []byte("smart_health_enabled")) {
		t.Fatalf("warnings = %#v", dump.Warnings)
	}
}

// EncryptConfigDumpForTest keeps test setup independent of the database global.
func EncryptConfigDumpForTest(dump *model.ConfigDump, password []byte) ([]byte, error) {
	plaintext, err := json.Marshal(dump)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	return EncryptDBBackup(plaintext, password, ConfigBackupMaxBytes)
}
