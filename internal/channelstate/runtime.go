package channelstate

import (
	"strings"

	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/relay/balancer"
)

// Invalidate clears every channel-derived runtime state after a configuration
// change. Normal updates, self-healing apply/rollback, enable, and delete must
// all call this shared path.
func Invalidate(channelID int, channels ...*model.Channel) {
	balancer.InvalidateChannel(channelID)
	relay.InvalidateRuntimeURLState(channelID)

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
}
