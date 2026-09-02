package op

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

// newHealthTestChannel 建立一个带两个凭据的渠道, 并把它放进运行时缓存,
// 使健康态写回可以命中与 relay 相同的读取路径。
func newHealthTestChannel(t *testing.T) *model.Channel {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "health.db"), false); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	channel := &model.Channel{
		Name: "health-channel",
		Type: model.ChannelProviderOpenAI,
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "key-one"}, {Enabled: true, ChannelKey: "key-two"}},
	}
	if err := ChannelCreate(channel, context.Background()); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	var stored model.Channel
	if err := db.GetDB().Preload("Keys").First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load channel: %v", err)
	}
	channelCache.Set(stored.ID, stored)
	t.Cleanup(func() {
		channelKeyHealthLock.Lock()
		channelKeyHealthNeedUpdate = make(map[int]struct{})
		channelKeyHealthLock.Unlock()
	})
	return &stored
}

func TestChannelKeyHealthUpdateCoolsDownRateLimitedKey(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	before := time.Now().Unix()
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID:  channel.ID,
		KeyID:      target.ID,
		StatusCode: http.StatusTooManyRequests,
		RetryAfter: 45,
	})
	cached, ok := channelCache.Get(channel.ID)
	if !ok {
		t.Fatal("channel missing from cache")
	}
	updated := findKey(t, cached, target.ID)
	if updated.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want 429", updated.StatusCode)
	}
	if updated.RetryAfterUntil < before+45 {
		t.Fatalf("retry after until = %d, want >= %d", updated.RetryAfterUntil, before+45)
	}
	if updated.IsAvailable(time.Now().Unix()) {
		t.Fatal("cooled key should not be available")
	}
	// 冷却只作用于命中的凭据, 同渠道另一个凭据必须仍然可用。
	if sibling := findKey(t, cached, channel.Keys[1].ID); !sibling.IsAvailable(time.Now().Unix()) {
		t.Fatal("sibling key should stay available")
	}
}

// 上游未给 Retry-After 时套用缺省冷却, 而不是让凭据立即重回轮换。
func TestChannelKeyHealthUpdateAppliesDefaultCooldown(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	before := time.Now().Unix()
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID:  channel.ID,
		KeyID:      target.ID,
		StatusCode: http.StatusTooManyRequests,
	})
	cached, _ := channelCache.Get(channel.ID)
	updated := findKey(t, cached, target.ID)
	if updated.RetryAfterUntil < before+defaultKeyCooldownSeconds {
		t.Fatalf("retry after until = %d, want >= %d", updated.RetryAfterUntil, before+defaultKeyCooldownSeconds)
	}
}

// 超长 Retry-After 被截断, 避免一次限流让凭据长期退出轮换。
func TestChannelKeyHealthUpdateClampsExcessiveRetryAfter(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	before := time.Now().Unix()
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID:  channel.ID,
		KeyID:      target.ID,
		StatusCode: http.StatusTooManyRequests,
		RetryAfter: 30 * 24 * 60 * 60,
	})
	cached, _ := channelCache.Get(channel.ID)
	if updated := findKey(t, cached, target.ID); updated.RetryAfterUntil > before+maxKeyCooldownSeconds+5 {
		t.Fatalf("retry after until = %d, want <= %d", updated.RetryAfterUntil, before+maxKeyCooldownSeconds+5)
	}
}

// 成功结果解除既有冷却, 使恢复的凭据立即重新参与轮换。
func TestChannelKeyHealthUpdateSuccessClearsCooldown(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: target.ID, StatusCode: http.StatusTooManyRequests, RetryAfter: 600,
	})
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: target.ID, StatusCode: http.StatusOK,
	})
	cached, _ := channelCache.Get(channel.ID)
	updated := findKey(t, cached, target.ID)
	if updated.RetryAfterUntil != 0 || updated.StatusCode != http.StatusOK {
		t.Fatalf("recovered key = %#v", updated)
	}
	if !updated.IsAvailable(time.Now().Unix()) {
		t.Fatal("recovered key should be available")
	}
}

// 5xx 属于瞬时故障, 由 relay failover 处理, 不应冷却凭据。
func TestChannelKeyHealthUpdateServerErrorDoesNotCoolDown(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: target.ID, StatusCode: http.StatusBadGateway,
	})
	cached, _ := channelCache.Get(channel.ID)
	updated := findKey(t, cached, target.ID)
	if updated.RetryAfterUntil != 0 {
		t.Fatalf("retry after until = %d, want 0", updated.RetryAfterUntil)
	}
	if !updated.IsAvailable(time.Now().Unix()) {
		t.Fatal("key should stay available after 5xx")
	}
}

// KeyID 为 0 表示渠道沿用 Channel.Key, 没有可写回的凭据行。
func TestChannelKeyHealthUpdateIgnoresLegacyKey(t *testing.T) {
	channel := newHealthTestChannel(t)
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: 0, StatusCode: http.StatusTooManyRequests, RetryAfter: 60,
	})
	channelKeyHealthLock.Lock()
	pending := len(channelKeyHealthNeedUpdate)
	channelKeyHealthLock.Unlock()
	if pending != 0 {
		t.Fatalf("pending writes = %d, want 0", pending)
	}
}

func TestChannelKeyHealthSaveDBPersistsCooldown(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: target.ID, StatusCode: http.StatusTooManyRequests, RetryAfter: 120,
	})
	if err := ChannelKeyHealthSaveDB(context.Background()); err != nil {
		t.Fatalf("save channel key health: %v", err)
	}
	var stored model.ChannelKey
	if err := db.GetDB().First(&stored, target.ID).Error; err != nil {
		t.Fatalf("load key: %v", err)
	}
	if stored.StatusCode != http.StatusTooManyRequests || stored.RetryAfterUntil == 0 {
		t.Fatalf("persisted key = %#v", stored)
	}
	if stored.ChannelKey != target.ChannelKey || !stored.Enabled {
		t.Fatalf("persisted key clobbered config: %#v", stored)
	}
	// 待写集合已清空, 重复保存不应再产生写入。
	channelKeyHealthLock.Lock()
	pending := len(channelKeyHealthNeedUpdate)
	channelKeyHealthLock.Unlock()
	if pending != 0 {
		t.Fatalf("pending writes after save = %d, want 0", pending)
	}
	if err := ChannelKeyHealthSaveDB(context.Background()); err != nil {
		t.Fatalf("second save: %v", err)
	}
}

// 界面的冷却提示读 ChannelList 下发的 Keys, 若快照丢掉凭据或冷却截止时间,
// 冷却就会变成静默生效, 排查时容易误判成渠道整体故障。
func TestChannelListExposesKeyCooldownState(t *testing.T) {
	channel := newHealthTestChannel(t)
	target := channel.Keys[0]
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: target.ID,
		StatusCode: http.StatusTooManyRequests, RetryAfter: 120,
	})

	var listed *model.Channel
	for _, item := range ChannelList() {
		if item.ID == channel.ID {
			listed = &item
			break
		}
	}
	if listed == nil {
		t.Fatalf("channel %d missing from ChannelList", channel.ID)
	}
	if len(listed.Keys) != len(channel.Keys) {
		t.Fatalf("listed keys = %d, want %d", len(listed.Keys), len(channel.Keys))
	}

	cooled := findKey(t, *listed, target.ID)
	now := time.Now().Unix()
	if cooled.RetryAfterUntil <= now {
		t.Errorf("cooled key retry_after_until = %d, want > %d", cooled.RetryAfterUntil, now)
	}
	if cooled.StatusCode != http.StatusTooManyRequests {
		t.Errorf("cooled key status = %d, want 429", cooled.StatusCode)
	}
	if other := findKey(t, *listed, channel.Keys[1].ID); other.RetryAfterUntil != 0 {
		t.Errorf("untouched key should not report cooldown, got %d", other.RetryAfterUntil)
	}
}

func findKey(t *testing.T, channel model.Channel, id int) model.ChannelKey {
	t.Helper()
	for _, key := range channel.Keys {
		if key.ID == id {
			return key
		}
	}
	t.Fatalf("key %d not found in channel %d", id, channel.ID)
	return model.ChannelKey{}
}
