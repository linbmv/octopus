package op

import (
	"context"
	"reflect"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

func TestCachedListsHaveStableOrder(t *testing.T) {
	ctx := context.Background()

	channels := cache.New[int, model.Channel](3)
	channels.Set(9, model.Channel{ID: 9})
	channels.Set(2, model.Channel{ID: 2})
	channelList, _ := NewChannelService(channels, nil).List(ctx)
	if got := []int{channelList[0].ID, channelList[1].ID}; !reflect.DeepEqual(got, []int{2, 9}) {
		t.Fatalf("channel order = %v", got)
	}

	groups := cache.New[int, model.Group](3)
	groups.Set(7, model.Group{ID: 7, Name: "z", Enabled: true})
	groups.Set(1, model.Group{ID: 1, Name: "a", Enabled: true})
	groupService := NewGroupService(groups, nil)
	groupList, _ := groupService.List(ctx)
	if got := []int{groupList[0].ID, groupList[1].ID}; !reflect.DeepEqual(got, []int{1, 7}) {
		t.Fatalf("group order = %v", got)
	}
	models, _ := groupService.ListModel(ctx)
	if !reflect.DeepEqual(models, []string{"a", "z"}) {
		t.Fatalf("group model order = %v", models)
	}

	settings := cache.New[model.SettingKey, string](3)
	settings.Set("z", "1")
	settings.Set("a", "2")
	settingList, _ := NewSettingsService(settings).List(ctx)
	if got := []model.SettingKey{settingList[0].Key, settingList[1].Key}; !reflect.DeepEqual(got, []model.SettingKey{"a", "z"}) {
		t.Fatalf("setting order = %v", got)
	}

	stats := NewStatsService()
	stats.apiKeys.Set(8, model.StatsAPIKey{APIKeyID: 8})
	stats.apiKeys.Set(3, model.StatsAPIKey{APIKeyID: 3})
	statsList := stats.APIKeyList()
	if got := []int{statsList[0].APIKeyID, statsList[1].APIKeyID}; !reflect.DeepEqual(got, []int{3, 8}) {
		t.Fatalf("stats API key order = %v", got)
	}
}

func TestGlobalCachedListsHaveStableOrder(t *testing.T) {
	oldAPIKeys, oldAPIKeyIDs, oldLLMs := apiKeyCache, apiKeyIDMap, llmModelCache
	apiKeyCache = cache.New[int, model.APIKey](3)
	apiKeyIDMap = cache.New[string, int](3)
	llmModelCache = cache.New[string, model.LLMPrice](3)
	t.Cleanup(func() {
		apiKeyCache, apiKeyIDMap, llmModelCache = oldAPIKeys, oldAPIKeyIDs, oldLLMs
	})

	apiKeyCache.Set(5, model.APIKey{ID: 5})
	apiKeyCache.Set(1, model.APIKey{ID: 1})
	apiKeys, _ := APIKeyList(context.Background())
	if got := []int{apiKeys[0].ID, apiKeys[1].ID}; !reflect.DeepEqual(got, []int{1, 5}) {
		t.Fatalf("API key order = %v", got)
	}

	llmModelCache.Set("z-model", model.LLMPrice{})
	llmModelCache.Set("a-model", model.LLMPrice{})
	llms, _ := LLMList(context.Background())
	if got := []string{llms[0].Name, llms[1].Name}; !reflect.DeepEqual(got, []string{"a-model", "z-model"}) {
		t.Fatalf("LLM order = %v", got)
	}
}
