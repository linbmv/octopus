package codexprovider

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/oauth"
)

var (
	providerCache sync.Map // map[string]*singleflightEntry
	mu            sync.Mutex
)

type singleflightEntry struct {
	provider oauth.TokenGetter
	inflight chan struct{} // nil = ready, non-nil = refreshing
}

// GetOrCreate returns a TokenProvider for the given key, creating or reusing one.
// Concurrent calls for the same (keyID, accessToken) will share a single provider
// and only one goroutine will create it (singleflight pattern).
// factory is called only when creating a new provider (allows custom OnRefreshed).
func GetOrCreate(keyID int, key model.ChannelKey, factory func() oauth.TokenGetter) oauth.TokenGetter {
	fingerprint := fmt.Sprintf("k%d_%s", keyID, shortHash(key.CodexAccessToken))

	for {
		if val, ok := providerCache.Load(fingerprint); ok {
			entry := val.(*singleflightEntry)
			if entry.inflight == nil {
				return entry.provider
			}
			<-entry.inflight
			continue
		}

		mu.Lock()
		placeholder := &singleflightEntry{inflight: make(chan struct{})}
		if val, loaded := providerCache.LoadOrStore(fingerprint, placeholder); loaded {
			mu.Unlock()
			entry := val.(*singleflightEntry)
			<-entry.inflight
			continue
		}
		mu.Unlock()

		provider := factory()
		placeholder.provider = provider
		close(placeholder.inflight)
		placeholder.inflight = nil

		return provider
	}
}

// InvalidateByKey removes the cached provider for the given key and old access token.
// Call this after successfully refreshing a token to prevent memory leaks.
func InvalidateByKey(keyID int, oldAccessToken string) {
	if oldAccessToken == "" {
		return
	}
	oldFingerprint := fmt.Sprintf("k%d_%s", keyID, shortHash(oldAccessToken))
	providerCache.Delete(oldFingerprint)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])[:12]
}
