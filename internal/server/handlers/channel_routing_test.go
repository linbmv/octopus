package handlers

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/routingstate"
)

func initChannelRoutingHandlerTestDB(t *testing.T) {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "channel-routing.db"), false); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache() error = %v", err)
	}
}

func TestCreateChannelAutoGroupsBeforeReturning(t *testing.T) {
	initChannelRoutingHandlerTestDB(t)
	ctx := context.Background()
	group := model.Group{Name: "instant-model", Mode: model.GroupModeFailover}
	if err := op.GroupCreate(&group, ctx); err != nil {
		t.Fatalf("GroupCreate() error = %v", err)
	}

	before := routingstate.Current()
	response := invokeHandler(http.MethodPost, "/channel", `{
		"name":"instant-channel",
		"type":"openai/chat_completions",
		"enabled":true,
		"base_urls":[{"url":"https://provider.example/v1"}],
		"keys":[{"enabled":true,"channel_key":"not-a-real-key"}],
		"model":"instant-model",
		"auto_group":2
	}`, createChannel)
	if response.Code != http.StatusOK {
		t.Fatalf("create channel status = %d; body=%s", response.Code, response.Body.String())
	}
	var channel model.Channel
	decodeResponseData(t, response, &channel)

	routable, err := op.GroupGetEnabledTree("instant-model", ctx)
	if err != nil {
		t.Fatalf("GroupGetEnabledTree() error = %v", err)
	}
	found := false
	for _, item := range routable.Items {
		if item.Type == model.GroupItemTypeChannel && item.ChannelID == channel.ID && item.ModelName == "instant-model" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created channel %d was not routable when create returned: %#v", channel.ID, routable.Items)
	}
	select {
	case <-before.Changed:
	default:
		t.Fatal("channel creation did not publish a routing change before returning")
	}
}

func TestEnableDisableChannelPublishesRoutingChangeBeforeReturning(t *testing.T) {
	initChannelRoutingHandlerTestDB(t)
	ctx := context.Background()
	channel := model.Channel{
		Name: "switch-channel", Type: "openai/chat_completions", Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.example/v1"}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "not-a-real-key"}},
		Model:    "switch-model",
	}
	if err := op.ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("ChannelCreate() error = %v", err)
	}

	for _, enabled := range []bool{false, true} {
		before := routingstate.Current()
		body := `{"id":` + jsonNumber(channel.ID) + `,"enabled":` + jsonBool(enabled) + `}`
		response := invokeHandler(http.MethodPost, "/channel/enable", body, enableChannel)
		if response.Code != http.StatusOK {
			t.Fatalf("set enabled=%t status = %d; body=%s", enabled, response.Code, response.Body.String())
		}
		stored, err := op.ChannelGet(channel.ID, ctx)
		if err != nil {
			t.Fatalf("ChannelGet() error = %v", err)
		}
		if stored.Enabled != enabled {
			t.Fatalf("stored enabled = %t, want %t", stored.Enabled, enabled)
		}
		select {
		case <-before.Changed:
		default:
			t.Fatalf("set enabled=%t returned before publishing routing change", enabled)
		}
	}
}

func jsonBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
