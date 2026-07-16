package op

import (
	"context"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestCapabilityEvidenceRanksChannelsAndKeysWithoutRemovingFallbacks(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	endpoint := "https://provider.test"
	channel := &model.Channel{
		ID: 11, Type: llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []model.BaseUrl{{URL: endpoint}},
		Keys: []model.ChannelKey{
			{ID: 101, ChannelID: 11, Enabled: true, ChannelKey: "negative"},
			{ID: 102, ChannelID: 11, Enabled: true, ChannelKey: "unknown"},
			{ID: 103, ChannelID: 11, Enabled: true, ChannelKey: "supported"},
		},
	}
	now := time.Now().UTC()
	writeCapabilityEvidence(t, ctx, channel, channel.Keys[0], endpoint, model.CapabilityTool, model.CapabilityNotImplemented, now, now.Add(time.Hour))
	writeCapabilityEvidence(t, ctx, channel, channel.Keys[2], endpoint, model.CapabilityTool, model.CapabilitySupported, now, now.Add(time.Hour))

	got := RankChannelKeysByCapability(ctx, channel, channel.Keys, "model-a", []model.Capability{model.CapabilityTool}, endpoint)
	if len(got) != 3 || got[0].ID != 103 || got[1].ID != 102 || got[2].ID != 101 {
		t.Fatalf("ranked keys = %#v, want supported/unknown/negative with every fallback retained", got)
	}
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityTool}, endpoint); rank != 0 {
		t.Fatalf("channel rank = %d, want supported tier 0", rank)
	}

	channel.Keys = channel.Keys[:2]
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityTool}, endpoint); rank != 1 {
		t.Fatalf("negative plus unknown rank = %d, want conservative tier 1", rank)
	}
	channel.Keys = channel.Keys[:1]
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityTool}, endpoint); rank != 2 {
		t.Fatalf("all-negative channel rank = %d, want tier 2", rank)
	}
}

func TestCapabilityEvidenceMustBeFreshAndMatchProtocolEndpointAndScope(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	endpoint := "https://provider.test"
	key := model.ChannelKey{ID: 201, ChannelID: 21, Enabled: true, ChannelKey: "account"}
	channel := &model.Channel{ID: 21, Type: llm.APIFormatOpenAIChatCompletion, BaseUrls: []model.BaseUrl{{URL: endpoint}}, Keys: []model.ChannelKey{key}}
	now := time.Now().UTC()

	writeCapabilityEvidence(t, ctx, channel, key, endpoint, model.CapabilityVision, model.CapabilitySupported, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityVision}, endpoint); rank != 1 {
		t.Fatalf("stale evidence rank = %d, want unknown", rank)
	}
	writeCapabilityEvidence(t, ctx, channel, key, "https://other.test", model.CapabilityVision, model.CapabilitySupported, now, now.Add(time.Hour))
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityVision}, endpoint); rank != 1 {
		t.Fatalf("other-endpoint evidence rank = %d, want unknown", rank)
	}

	otherProtocol := *channel
	otherProtocol.Type = llm.APIFormatAnthropicMessage
	writeCapabilityEvidence(t, ctx, &otherProtocol, key, endpoint, model.CapabilityVision, model.CapabilitySupported, now, now.Add(time.Hour))
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityVision}, endpoint); rank != 1 {
		t.Fatalf("other-protocol evidence rank = %d, want unknown", rank)
	}

	writeCapabilityEvidence(t, ctx, channel, key, endpoint, model.CapabilityVision, model.CapabilitySupported, now, now.Add(time.Hour))
	channel.UserAgent = "changed-after-probe"
	if rank := CapabilityChannelRank(ctx, channel, "model-a", []model.Capability{model.CapabilityVision}, endpoint); rank != 1 {
		t.Fatalf("scope-mismatched evidence rank = %d, want unknown", rank)
	}
}

func writeCapabilityEvidence(
	t *testing.T,
	ctx context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint string,
	capability model.Capability,
	status model.CapabilityStatus,
	probedAt, expiresAt time.Time,
) {
	t.Helper()
	evidence := model.CapabilityEvidence{
		ChannelID: channel.ID, ChannelKeyID: key.ID, Model: "model-a", WireProtocol: channel.Type,
		Capability: capability, Status: status, Endpoint: endpoint,
		EndpointFingerprint: model.CapabilityEndpointFingerprint(endpoint),
		ScopeFingerprint:    model.CapabilityScopeFingerprint(channel, key, endpoint),
		Source:              "probe", ProbedAt: probedAt, ExpiresAt: expiresAt,
	}
	if err := CapabilityEvidenceUpsert(ctx, &evidence); err != nil {
		t.Fatalf("upsert capability evidence: %v", err)
	}
}
