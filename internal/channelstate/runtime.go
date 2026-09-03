package channelstate

import (
	"strings"

	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/routingstate"
)

// Invalidate clears every channel-derived runtime state after a configuration
// change. Normal updates, enable, and delete all call this shared path.
func Invalidate(channelID int, channels ...*model.Channel) {
	balancer.InvalidateChannel(channelID)
	relay.InvalidateRuntimeURLState(channelID)
	relay.InvalidateChannelRuntimePenalties(channelID, "")

	seenProxyURLs := make(map[string]struct{}, len(channels))
	foundChannel := false
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		foundChannel = true
		if channel.ChannelProxy == nil {
			continue
		}
		proxyURL := strings.TrimSpace(*channel.ChannelProxy)
		if proxyURL == "" {
			continue
		}
		if _, exists := seenProxyURLs[proxyURL]; exists {
			continue
		}
		seenProxyURLs[proxyURL] = struct{}{}
		client.InvalidateCustomProxyClient(proxyURL)
	}
	if !foundChannel {
		client.InvalidateAllCustomProxyClients()
	}
	routingstate.Notify()
}

// InvalidateAll is the bulk-write counterpart to Invalidate. Database restore
// can replace every channel and key identity, so all derived runtime evidence
// must be dropped before waking in-flight requests.
func InvalidateAll() {
	balancer.InvalidateAll()
	relay.InvalidateAllRuntimeState()
	client.InvalidateAllCustomProxyClients()
	routingstate.Notify()
}
