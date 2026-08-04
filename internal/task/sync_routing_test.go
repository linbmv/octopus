package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/routingstate"
	"github.com/looplj/axonhub/llm"
)

func TestUpdateChannelModelsPublishesRoutingChangeWithoutGroupChanges(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "sync-routing.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatal(err)
	}

	channel := model.Channel{
		Name:     "sync-routing",
		Type:     llm.APIFormatOpenAIChatCompletion,
		Enabled:  true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test/v1"}},
		Model:    "old-model",
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "test-secret"}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatal(err)
	}

	before := routingstate.Current()
	if err := updateChannelModels(&channel, []string{"new-model"}, context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-before.Changed:
	default:
		t.Fatal("model synchronization did not publish a routing change")
	}
	after := routingstate.Current()
	if after.Revision != before.Revision+1 {
		t.Fatalf("routing revision advanced %d times, want exactly once", after.Revision-before.Revision)
	}
	if channel.Model != "new-model" {
		t.Fatalf("in-memory synchronized channel model = %q, want new-model", channel.Model)
	}
	stored, err := op.ChannelGet(channel.ID, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "new-model" || stored.ConfigVersion != channel.ConfigVersion+1 {
		t.Fatalf("stored synchronized channel = %#v", stored)
	}
}
