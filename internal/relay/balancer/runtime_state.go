package balancer

import "time"

const runtimeStateSweepEvery uint64 = 128

func stateExpired(lastUsed, now time.Time, ttl time.Duration) bool {
	return ttl <= 0 || now.Sub(lastUsed) > ttl
}

// InvalidateAPIKey removes request affinity associated with an API key after
// the key is changed or deleted.
func InvalidateAPIKey(apiKeyID int) {
	globalSession.invalidateAPIKey(apiKeyID)
}

// InvalidateChannel removes every balancer state entry that can reference a
// channel. It is intended for successful channel updates, disables or deletes.
func InvalidateChannel(channelID int) {
	globalSession.invalidateChannel(channelID)
	globalBreaker.invalidateChannel(channelID)
	smoothWeightedState.invalidateChannel(channelID)
	smoothRoundRobinState.invalidateChannel(channelID)
}

// InvalidateGroups removes state whose current key does not retain a group ID.
// Group mutations are rare, so clearing sticky and smooth-weighted state is the
// safest way to prevent routing with an obsolete group definition.
func InvalidateGroups() {
	globalSession.clear()
	smoothWeightedState.clear()
	smoothRoundRobinState.clear()
}
