package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

func TestChannelServiceReadMethodsUseInjectedCache(t *testing.T) {
	channels := cache.New[int, model.Channel](1)
	keys := cache.New[int, model.ChannelKey](1)
	channels.Set(7, model.Channel{
		ID:          7,
		Name:        "",
		Enabled:     true,
		Model:       "gpt-4",
		CustomModel: "gpt-4o",
	})
	service := NewChannelService(channels, keys)

	channel, err := service.Get(7, context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if channel.ID != 7 {
		t.Fatalf("Get ID = %d, want 7", channel.ID)
	}
	channel.Name = "mutated"
	again, err := service.Get(7, context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if again.Name != "" {
		t.Fatal("Get should return a copy of cached channel")
	}

	llmChannels, err := service.LLMList(context.Background())
	if err != nil {
		t.Fatalf("LLMList returned error: %v", err)
	}
	if len(llmChannels) != 2 {
		t.Fatalf("LLMList length = %d, want 2", len(llmChannels))
	}
	for _, item := range llmChannels {
		if item.ChannelName != "Channel 7" {
			t.Fatalf("ChannelName = %q, want fallback name", item.ChannelName)
		}
	}
}

func TestChannelLLMListFallbacksEmptyChannelName(t *testing.T) {
	// 模拟一个 name 为空的渠道（可能是旧数据或约束失效）
	emptyNameChannel := model.Channel{
		ID:      999,
		Name:    "", // 空名称
		Enabled: true,
		Model:   "gpt-4",
	}
	channelCache.Set(emptyNameChannel.ID, emptyNameChannel)
	defer channelCache.Del(emptyNameChannel.ID)

	llmChannels, err := ChannelLLMList(context.Background())
	if err != nil {
		t.Fatalf("ChannelLLMList 返回错误: %v", err)
	}

	// 查找对应的 LLMChannel
	var found *model.LLMChannel
	for i := range llmChannels {
		if llmChannels[i].ChannelID == emptyNameChannel.ID {
			found = &llmChannels[i]
			break
		}
	}

	if found == nil {
		t.Fatalf("未找到 channel_id=%d 的 LLMChannel", emptyNameChannel.ID)
	}

	// 验证 fallback 逻辑：空 name 应该被替换为 "Channel {ID}"
	expectedName := "Channel 999"
	if found.ChannelName != expectedName {
		t.Errorf("ChannelName = %q, want %q", found.ChannelName, expectedName)
	}

	// 验证其他字段未受影响
	if found.Name != "gpt-4" {
		t.Errorf("Name = %q, want gpt-4", found.Name)
	}
	if !found.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestChannelLLMListPreservesNonEmptyChannelName(t *testing.T) {
	// 正常渠道应该保留原始名称
	normalChannel := model.Channel{
		ID:      998,
		Name:    "My OpenAI Channel",
		Enabled: true,
		Model:   "gpt-3.5-turbo",
	}
	channelCache.Set(normalChannel.ID, normalChannel)
	defer channelCache.Del(normalChannel.ID)

	llmChannels, err := ChannelLLMList(context.Background())
	if err != nil {
		t.Fatalf("ChannelLLMList 返回错误: %v", err)
	}

	var found *model.LLMChannel
	for i := range llmChannels {
		if llmChannels[i].ChannelID == normalChannel.ID {
			found = &llmChannels[i]
			break
		}
	}

	if found == nil {
		t.Fatalf("未找到 channel_id=%d 的 LLMChannel", normalChannel.ID)
	}

	// 正常名称应该原样保留
	if found.ChannelName != normalChannel.Name {
		t.Errorf("ChannelName = %q, want %q", found.ChannelName, normalChannel.Name)
	}
}
