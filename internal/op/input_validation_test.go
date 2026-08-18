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

func TestChannelOperationsRejectDuplicateCredentials(t *testing.T) {
	initTestDB(t)

	channel := validOperationChannel("duplicate-create")
	channel.Keys = append(channel.Keys, model.ChannelKey{Enabled: true, ChannelKey: " secret "})
	if err := ChannelCreate(&channel, context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("ChannelCreate() error = %v, want ErrConflict", err)
	}

	channel = validOperationChannel("duplicate-update")
	if err := ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("ChannelCreate() error = %v", err)
	}
	_, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:        channel.ID,
		KeysToAdd: []model.ChannelKeyAddRequest{{Enabled: true, ChannelKey: "secret"}},
	}, context.Background())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ChannelUpdate() error = %v, want ErrConflict", err)
	}
}

func TestAPIOperationsRequireExistingGroupReferences(t *testing.T) {
	initTestDB(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()

	missing := model.APIKey{Name: "missing-group", APIKey: "sk-missing-group", SupportedModels: "not-created"}
	if err := APIKeyCreate(&missing, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("APIKeyCreate() error = %v, want ErrInvalidInput", err)
	}

	group := model.Group{Name: "created-group", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(&group, context.Background()); err != nil {
		t.Fatalf("GroupCreate() error = %v", err)
	}
	key := model.APIKey{Name: "valid-group", APIKey: "sk-valid-group", SupportedModels: group.Name}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() with enabled group error = %v", err)
	}
	key.SupportedModels = "deleted-group"
	if err := APIKeyUpdate(&key, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("APIKeyUpdate() error = %v, want ErrInvalidInput", err)
	}
}

func TestAPIKeySupportedModelsAcceptRuntimeSuffixVariants(t *testing.T) {
	initTestDB(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()

	group := model.Group{Name: "suffix-base", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(&group, context.Background()); err != nil {
		t.Fatalf("GroupCreate() error = %v", err)
	}
	// 运行时 GetEnabledTree 会剥离能力后缀后再匹配分组，写入校验必须同样放行。
	key := model.APIKey{
		Name:            "suffix-key",
		APIKey:          "sk-suffix-key",
		SupportedModels: "suffix-base-thinking,suffix-base[1m],suffix-base",
	}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() with suffix variants error = %v", err)
	}
}

func TestAPIKeyUpdateGrandfathersLegacySupportedModels(t *testing.T) {
	initTestDB(t)
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()

	group := model.Group{Name: "legacy-group", Mode: model.GroupModeRoundRobin}
	if err := GroupCreate(&group, context.Background()); err != nil {
		t.Fatalf("GroupCreate() error = %v", err)
	}
	key := model.APIKey{Name: "legacy-key", APIKey: "sk-legacy-key", SupportedModels: group.Name}
	if err := APIKeyCreate(&key, context.Background()); err != nil {
		t.Fatalf("APIKeyCreate() error = %v", err)
	}

	// 模拟旧备份导入产生的失效引用：直接写库并同步缓存，绕过写入校验。
	legacyModels := group.Name + ",grok"
	if err := db.GetDB().Model(&model.APIKey{}).Where("id = ?", key.ID).
		Update("supported_models", legacyModels).Error; err != nil {
		t.Fatalf("seed legacy supported_models: %v", err)
	}
	seeded := key
	seeded.SupportedModels = legacyModels
	apiKeyCache.Set(seeded.ID, seeded)

	// 保留历史失效条目、只改其他字段：必须成功，不能被 grok 卡死。
	update := seeded
	update.Name = "legacy-key-renamed"
	if err := APIKeyUpdate(&update, context.Background()); err != nil {
		t.Fatalf("APIKeyUpdate() keeping legacy entry error = %v", err)
	}

	// 新增另一个未知分组引用：仍然拒绝。
	update.SupportedModels = legacyModels + ",another-unknown"
	if err := APIKeyUpdate(&update, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("APIKeyUpdate() adding unknown entry error = %v, want ErrInvalidInput", err)
	}

	// 删除历史失效条目后再想加回来：按新规则拒绝。
	update.SupportedModels = group.Name
	if err := APIKeyUpdate(&update, context.Background()); err != nil {
		t.Fatalf("APIKeyUpdate() removing legacy entry error = %v", err)
	}
	update.SupportedModels = legacyModels
	if err := APIKeyUpdate(&update, context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("APIKeyUpdate() re-adding removed legacy entry error = %v, want ErrInvalidInput", err)
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

func TestChannelFirstTokenTimeoutExceptionPersistsAcrossCreateAndPatch(t *testing.T) {
	initTestDB(t)
	channel := validOperationChannel("slow-trusted-channel")
	channel.FirstTokenTimeoutExceptionEnabled = true
	channel.FirstTokenTimeoutExceptionSeconds = 200
	if err := ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("ChannelCreate() error = %v", err)
	}

	persisted, err := ChannelGet(channel.ID, context.Background())
	if err != nil {
		t.Fatalf("ChannelGet() error = %v", err)
	}
	if !persisted.FirstTokenTimeoutExceptionEnabled || persisted.FirstTokenTimeoutExceptionSeconds != 200 {
		t.Fatalf("created channel exception = enabled:%v seconds:%d, want true/200", persisted.FirstTokenTimeoutExceptionEnabled, persisted.FirstTokenTimeoutExceptionSeconds)
	}

	disabled := false
	seconds := 300
	updated, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:                                channel.ID,
		FirstTokenTimeoutExceptionEnabled: &disabled,
		FirstTokenTimeoutExceptionSeconds: &seconds,
	}, context.Background())
	if err != nil {
		t.Fatalf("ChannelUpdate() error = %v", err)
	}
	if updated.FirstTokenTimeoutExceptionEnabled || updated.FirstTokenTimeoutExceptionSeconds != 300 {
		t.Fatalf("updated channel exception = enabled:%v seconds:%d, want false/300", updated.FirstTokenTimeoutExceptionEnabled, updated.FirstTokenTimeoutExceptionSeconds)
	}

	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load persisted channel: %v", err)
	}
	if stored.FirstTokenTimeoutExceptionEnabled || stored.FirstTokenTimeoutExceptionSeconds != 300 {
		t.Fatalf("persisted channel exception = enabled:%v seconds:%d, want false/300", stored.FirstTokenTimeoutExceptionEnabled, stored.FirstTokenTimeoutExceptionSeconds)
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
