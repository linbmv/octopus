package handlers

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestDecodeDBDumpConvertsPlaintextEdgeV2(t *testing.T) {
	body := []byte(`{
  "version": 2,
  "channels": [{
    "id": 10,
    "name": "legacy-channel",
    "type": "openai/chat_completions",
    "enabled": true,
    "base_urls": [{"url": "https://upstream.example/v1"}],
    "model": "gpt-test"
  }],
  "groups": [{"id": 20, "name": "legacy-group", "enabled": true, "mode": 3}],
  "group_items": [{"id": 30, "group_id": 20, "type": "channel", "channel_id": 10, "model_name": "gpt-test", "priority": 1}]
}`)

	var dump model.DBDump
	if err := decodeDBDump(body, &dump); err != nil {
		t.Fatalf("decode plaintext Edge v2 dump: %v", err)
	}
	if dump.Version != 3 || dump.Scope != model.ConfigDumpScope {
		t.Fatalf("converted dump header = version %d scope %q", dump.Version, dump.Scope)
	}
	if len(dump.Channels) != 1 || len(dump.ChannelModels) != 1 || len(dump.Groups) != 1 || len(dump.GroupItems) != 1 {
		t.Fatalf("converted dump = %#v", dump)
	}
	if dump.Groups[0].Mode != model.GroupModeFailover {
		t.Fatalf("converted group mode = %q, want %q", dump.Groups[0].Mode, model.GroupModeFailover)
	}
}
