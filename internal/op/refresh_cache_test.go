package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestFullCacheRefreshRemovesDeletedRows(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	channelCache.Set(999, model.Channel{ID: 999})
	channelKeyCache.Set(999, model.ChannelKey{ID: 999})
	if err := channelRefreshCache(ctx); err != nil {
		t.Fatalf("channelRefreshCache: %v", err)
	}
	if _, ok := channelCache.Get(999); ok {
		t.Fatal("stale channel survived full refresh")
	}
	if _, ok := channelKeyCache.Get(999); ok {
		t.Fatal("stale channel key survived full refresh")
	}

	groupCache.Set(999, model.Group{ID: 999, Name: "stale"})
	groupMap.Set("stale", model.Group{ID: 999, Name: "stale"})
	if err := groupRefreshCache(ctx); err != nil {
		t.Fatalf("groupRefreshCache: %v", err)
	}
	if _, ok := groupCache.Get(999); ok {
		t.Fatal("stale group survived full refresh")
	}
	if _, ok := groupMap.Get("stale"); ok {
		t.Fatal("stale group name index survived full refresh")
	}

	apiKeyCache.Set(999, model.APIKey{ID: 999, APIKey: "stale-key"})
	apiKeyIDMap.Set("stale-key", 999)
	if err := apiKeyRefreshCache(ctx); err != nil {
		t.Fatalf("apiKeyRefreshCache: %v", err)
	}
	if _, ok := apiKeyCache.Get(999); ok {
		t.Fatal("stale API key survived full refresh")
	}
	if _, ok := apiKeyIDMap.Get("stale-key"); ok {
		t.Fatal("stale API key reverse index survived full refresh")
	}

	llmModelCache.Set("stale-model", model.LLMPrice{})
	if err := llmRefreshCache(ctx); err != nil {
		t.Fatalf("llmRefreshCache: %v", err)
	}
	if _, ok := llmModelCache.Get("stale-model"); ok {
		t.Fatal("stale LLM survived full refresh")
	}

	staleSetting := model.SettingKey("stale-setting")
	settingCache.Set(staleSetting, "value")
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache: %v", err)
	}
	if _, ok := settingCache.Get(staleSetting); ok {
		t.Fatal("stale setting survived full refresh")
	}
}
