package model

import (
	"testing"

	"github.com/looplj/axonhub/llm"
)

func TestCapabilityScopeFingerprintIncludesRawPassthrough(t *testing.T) {
	channel := &Channel{ID: 1, Type: llm.APIFormatOpenAIResponse}
	key := ChannelKey{ID: 2, ChannelID: 1, ChannelKey: "secret"}
	withoutRaw := CapabilityScopeFingerprint(channel, key, "https://provider.test")
	channel.RawPassthrough = true
	withRaw := CapabilityScopeFingerprint(channel, key, "https://provider.test")
	if withoutRaw == withRaw {
		t.Fatal("raw_passthrough change did not invalidate capability/baseline scope")
	}
}
