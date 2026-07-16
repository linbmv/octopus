package op

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func validOperationChannel(name string) model.Channel {
	return model.Channel{
		Name:     name,
		Type:     llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []model.BaseUrl{{URL: "https://example.com"}},
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "secret"}},
	}
}

func TestOperationLayerRejectsInvalidBusinessModels(t *testing.T) {
	initTestDB(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()
	llmModelCache.Clear()

	channel := validOperationChannel(" ")
	if err := ChannelCreate(&channel, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ChannelCreate() error = %v, want ErrInvalidInput", err)
	}
	group := model.Group{Name: "group", Mode: 0}
	if err := GroupCreate(&group, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GroupCreate() error = %v, want ErrInvalidInput", err)
	}
	key := model.APIKey{Name: "key", APIKey: "generated", MaxCost: math.NaN()}
	if err := APIKeyCreate(&key, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("APIKeyCreate() error = %v, want ErrInvalidInput", err)
	}
	info := model.LLMInfo{Name: "model", LLMPrice: model.LLMPrice{Input: -1}}
	if err := LLMCreate(info, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("LLMCreate() error = %v, want ErrInvalidInput", err)
	}

	for table, target := range map[string]any{
		"channels":  &model.Channel{},
		"groups":    &model.Group{},
		"api_keys":  &model.APIKey{},
		"llm_infos": &model.LLMInfo{},
	} {
		var count int64
		if err := db.GetDB().Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s contains %d rows after rejected input", table, count)
		}
	}
}

func TestChannelUpdateMissingKeyRollsBackPatch(t *testing.T) {
	initTestDB(t)
	channel := validOperationChannel("before")
	if err := ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("ChannelCreate() error = %v", err)
	}
	after := "after"
	_, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:           channel.ID,
		Name:         &after,
		KeysToDelete: []int{999999},
	}, context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ChannelUpdate() error = %v, want ErrNotFound", err)
	}
	var persisted model.Channel
	if err := db.GetDB().First(&persisted, channel.ID).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	if persisted.Name != "before" {
		t.Fatalf("channel name = %q, want rollback to before", persisted.Name)
	}
}

func TestChannelAdvancedRewriteRulesPersistAcrossCreateAndPatch(t *testing.T) {
	initTestDB(t)
	value := `0.2`
	channel := validOperationChannel("rewrite")
	channel.HeaderRules = []model.HeaderRule{{Action: "append", HeaderKey: "X-Trace", HeaderValue: "one"}}
	channel.JSONRewriteRules = []model.JSONRewriteRule{{Action: "override", Path: "/temperature", Value: &value}}
	if err := ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("ChannelCreate() error = %v", err)
	}

	replacement := []model.HeaderRule{{Action: "remove", HeaderKey: "X-Legacy"}}
	removeJSON := []model.JSONRewriteRule{{Action: "remove", Path: "/metadata/internal"}}
	baseURLs := []model.BaseUrl{{URL: "https://second.example.com", Delay: 7}}
	customHeaders := []model.CustomHeader{{HeaderKey: "X-Custom", HeaderValue: "value"}}
	updated, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:               channel.ID,
		BaseUrls:         &baseURLs,
		CustomHeader:     &customHeaders,
		HeaderRules:      &replacement,
		JSONRewriteRules: &removeJSON,
	}, context.Background())
	if err != nil {
		t.Fatalf("ChannelUpdate() error = %v", err)
	}
	if len(updated.HeaderRules) != 1 || updated.HeaderRules[0].Action != "remove" {
		t.Fatalf("updated header rules = %#v", updated.HeaderRules)
	}
	if len(updated.JSONRewriteRules) != 1 || updated.JSONRewriteRules[0].Path != "/metadata/internal" {
		t.Fatalf("updated JSON rules = %#v", updated.JSONRewriteRules)
	}
	if len(updated.BaseUrls) != 1 || updated.BaseUrls[0].URL != "https://second.example.com" || len(updated.CustomHeader) != 1 {
		t.Fatalf("updated legacy JSON fields: base_urls=%#v custom_header=%#v", updated.BaseUrls, updated.CustomHeader)
	}

	var persisted model.Channel
	if err := db.GetDB().First(&persisted, channel.ID).Error; err != nil {
		t.Fatalf("load persisted channel: %v", err)
	}
	if len(persisted.HeaderRules) != 1 || persisted.HeaderRules[0].HeaderKey != "X-Legacy" {
		t.Fatalf("persisted header rules = %#v", persisted.HeaderRules)
	}
	if len(persisted.JSONRewriteRules) != 1 || persisted.JSONRewriteRules[0].Action != "remove" {
		t.Fatalf("persisted JSON rules = %#v", persisted.JSONRewriteRules)
	}
}

func TestGroupUpdateMissingItemRollsBackPatch(t *testing.T) {
	initTestDB(t)
	group := model.Group{Name: "before", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(&group, context.Background()); err != nil {
		t.Fatalf("GroupCreate() error = %v", err)
	}
	after := "after"
	_, err := GroupUpdate(&model.GroupUpdateRequest{
		ID:            group.ID,
		Name:          &after,
		ItemsToDelete: []int{999999},
	}, context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GroupUpdate() error = %v, want ErrNotFound", err)
	}
	var persisted model.Group
	if err := db.GetDB().First(&persisted, group.ID).Error; err != nil {
		t.Fatalf("load group: %v", err)
	}
	if persisted.Name != "before" {
		t.Fatalf("group name = %q, want rollback to before", persisted.Name)
	}
}
