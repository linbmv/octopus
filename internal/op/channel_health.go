package op

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

// defaultKeyCooldownSeconds 是上游返回 429 但未给出 Retry-After 时的保守冷却时长。
// 与 model.ChannelKey.IsAvailable 中缺省判定保持一致。
const defaultKeyCooldownSeconds int64 = int64(5 * time.Minute / time.Second)

// maxKeyCooldownSeconds 限制上游给出的超长 Retry-After，避免一次限流让凭据长期不可用。
const maxKeyCooldownSeconds int64 = int64(time.Hour / time.Second)

var channelKeyHealthNeedUpdate = make(map[int]struct{}) // 等待持久化的渠道凭据 ID。
var channelKeyHealthLock sync.Mutex                     // 保护凭据健康态写入和待写集合。

// ChannelKeyHealthReport 记录一次上游调用在具体凭据上的结果。statusCode 为 0
// 表示网络层失败等没有 HTTP 状态码的情况。
type ChannelKeyHealthReport struct {
	ChannelID  int
	KeyID      int
	StatusCode int
	RetryAfter int64 // 上游 Retry-After 秒数，0 表示未提供。
}

// ChannelKeyHealthUpdate 将一次调用结果写入凭据健康态。仅 429 会真正冷却凭据，
// 其余状态码只更新最近使用时间：短暂的 5xx 由 relay failover 处理，不应让凭据整体退出轮换。
func ChannelKeyHealthUpdate(report ChannelKeyHealthReport) {
	if report.KeyID <= 0 {
		return
	}
	channelKeyHealthLock.Lock()
	defer channelKeyHealthLock.Unlock()

	channel, ok := channelCache.Get(report.ChannelID)
	if !ok {
		return
	}
	now := time.Now().Unix()
	updated := false
	for index := range channel.Keys {
		if channel.Keys[index].ID != report.KeyID {
			continue
		}
		key := &channel.Keys[index]
		key.LastUseTimeStamp = now
		key.StatusCode = report.StatusCode
		if report.StatusCode == http.StatusTooManyRequests {
			cooldown := report.RetryAfter
			if cooldown <= 0 {
				cooldown = defaultKeyCooldownSeconds
			}
			if cooldown > maxKeyCooldownSeconds {
				cooldown = maxKeyCooldownSeconds
			}
			key.RetryAfterUntil = now + cooldown
		} else {
			// 非 429 结果解除既有冷却，让恢复的凭据立即重新参与轮换。
			key.RetryAfterUntil = 0
		}
		updated = true
		break
	}
	if !updated {
		return
	}
	channelCache.Set(channel.ID, channel)
	channelKeyHealthNeedUpdate[report.KeyID] = struct{}{}
}

// ChannelKeyHealthSaveDB 持久化本批凭据健康态。写入失败时恢复待写标记，
// 使下一轮任务重试，与统计持久化的处理方式一致。
func ChannelKeyHealthSaveDB(ctx context.Context) error {
	channelKeyHealthLock.Lock()
	keyIDs := make([]int, 0, len(channelKeyHealthNeedUpdate))
	for id := range channelKeyHealthNeedUpdate {
		keyIDs = append(keyIDs, id)
	}
	channelKeyHealthNeedUpdate = make(map[int]struct{})
	pending := make(map[int]model.ChannelKey, len(keyIDs))
	for _, channel := range channelCache.GetAll() {
		for _, key := range channel.Keys {
			for _, id := range keyIDs {
				if key.ID == id {
					pending[id] = key
				}
			}
		}
	}
	channelKeyHealthLock.Unlock()

	if len(pending) == 0 {
		return nil
	}
	if err := persistChannelKeyHealth(ctx, pending); err != nil {
		channelKeyHealthLock.Lock()
		for id := range pending {
			channelKeyHealthNeedUpdate[id] = struct{}{}
		}
		channelKeyHealthLock.Unlock()
		return err
	}
	return nil
}

func persistChannelKeyHealth(ctx context.Context, pending map[int]model.ChannelKey) error {
	conn := db.GetDB().WithContext(ctx)
	if !conn.Migrator().HasTable(&model.ChannelKey{}) {
		return nil
	}
	return conn.Transaction(func(tx *gorm.DB) error {
		for _, key := range pending {
			if result := tx.Model(&model.ChannelKey{}).
				Where("id = ?", key.ID).
				Select("status_code", "last_use_time_stamp", "retry_after_until").
				Updates(&key); result.Error != nil {
				return result.Error
			}
		}
		return nil
	})
}
