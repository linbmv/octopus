package relay

import (
	"strings"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func failoverTestChannel(version int) *dbmodel.Channel {
	return &dbmodel.Channel{
		ID: 41, ConfigVersion: version, Type: llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://primary.example/v1"}, {URL: "https://backup.example/v1"}},
	}
}

func TestRequestFailoverStateScopesFailuresWithoutLeakingCredentials(t *testing.T) {
	channel := failoverTestChannel(3)
	keyOne := dbmodel.ChannelKey{ID: 7, ChannelKey: "credential-one"}
	keyTwo := dbmodel.ChannelKey{ID: 8, ChannelKey: "credential-two"}
	primary := newRequestCandidateID(channel, keyOne, "model-a", channel.BaseUrls[0].URL)
	backup := newRequestCandidateID(channel, keyOne, "model-a", channel.BaseUrls[1].URL)
	otherKey := newRequestCandidateID(channel, keyTwo, "model-a", channel.BaseUrls[0].URL)

	if primary.scopeFingerprint == "" || strings.Contains(primary.scopeFingerprint, keyOne.ChannelKey) {
		t.Fatalf("candidate fingerprint is missing or contains credential material: %q", primary.scopeFingerprint)
	}

	t.Run("endpoint failure keeps other endpoint and key eligible", func(t *testing.T) {
		state := newRequestFailoverState()
		if action, ok := state.selectCandidate(primary); !ok || action != requestFailoverSelect {
			t.Fatalf("initial selection = (%q, %t)", action, ok)
		}
		state.exhaust(primary, requestFailureCandidate)
		if state.allows(primary) {
			t.Fatal("failed endpoint remained eligible")
		}
		if !state.allows(backup) || !state.allows(otherKey) {
			t.Fatal("endpoint-scoped failure blocked an unrelated endpoint or key")
		}
		if action, ok := state.selectCandidate(backup); !ok || action != requestFailoverRetryBaseURL {
			t.Fatalf("endpoint transition = (%q, %t)", action, ok)
		}
	})

	t.Run("key failure excludes key across endpoints", func(t *testing.T) {
		state := newRequestFailoverState()
		state.exhaust(primary, requestFailureKey)
		if state.allows(primary) || state.allows(backup) {
			t.Fatal("key-scoped failure did not exclude every endpoint for the key")
		}
		if !state.allows(otherKey) {
			t.Fatal("key-scoped failure blocked another key")
		}
	})

	t.Run("channel failure expires when configuration version changes", func(t *testing.T) {
		state := newRequestFailoverState()
		state.exhaust(primary, requestFailureChannel)
		if state.allows(primary) || state.allows(otherKey) {
			t.Fatal("channel-scoped failure did not exclude the current channel version")
		}
		updated := newRequestCandidateID(failoverTestChannel(4), keyOne, "model-a", channel.BaseUrls[0].URL)
		if !state.allows(updated) {
			t.Fatal("administrator-updated channel version remained excluded")
		}
	})
}

func TestRequestFailoverStateTracksBoundedTransitionActions(t *testing.T) {
	state := newRequestFailoverState()
	channel := failoverTestChannel(1)
	first := newRequestCandidateID(channel, dbmodel.ChannelKey{ID: 1, ChannelKey: "a"}, "m", channel.BaseUrls[0].URL)
	secondURL := newRequestCandidateID(channel, dbmodel.ChannelKey{ID: 1, ChannelKey: "a"}, "m", channel.BaseUrls[1].URL)
	secondKey := newRequestCandidateID(channel, dbmodel.ChannelKey{ID: 2, ChannelKey: "b"}, "m", channel.BaseUrls[1].URL)
	otherChannel := newRequestCandidateID(&dbmodel.Channel{
		ID: 42, ConfigVersion: 1, Type: llm.APIFormatOpenAIChatCompletion,
	}, dbmodel.ChannelKey{ID: 3, ChannelKey: "c"}, "m", "https://other.example/v1")

	transitions := []struct {
		candidate requestCandidateID
		want      requestFailoverAction
	}{
		{first, requestFailoverSelect},
		{secondURL, requestFailoverRetryBaseURL},
		{secondKey, requestFailoverRetryKey},
		{otherChannel, requestFailoverRetryChannel},
	}
	for _, transition := range transitions {
		if got, ok := state.selectCandidate(transition.candidate); !ok || got != transition.want {
			t.Fatalf("selectCandidate() = (%q, %t), want (%q, true)", got, ok, transition.want)
		}
	}
	if state.switches != len(transitions)-1 {
		t.Fatalf("switches = %d, want %d", state.switches, len(transitions)-1)
	}
	state.routingChanged()
	if state.lastAction != requestFailoverRoutingChange {
		t.Fatalf("routing action = %q", state.lastAction)
	}
	state.stop()
	if state.lastAction != requestFailoverStop {
		t.Fatalf("stop action = %q", state.lastAction)
	}
}
