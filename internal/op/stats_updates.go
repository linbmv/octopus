package op

import (
	"context"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func StatsDailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	return statsService.DailyUpdate(ctx, metrics)
}

func (s *StatsService) DailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	today := time.Now().Format("20060102")

	s.dailyMu.Lock()
	if s.daily.Date == today {
		s.daily.Add(metrics)
		s.dailyMu.Unlock()
		return nil
	}

	prevDaily := s.daily
	s.daily = model.StatsDaily{Date: today}
	s.daily.Add(metrics)
	s.dailyMu.Unlock()

	s.signalSave(prevDaily)
	return nil
}

func StatsTotalUpdate(metrics model.StatsMetrics) error {
	return statsService.TotalUpdate(metrics)
}

func (s *StatsService) TotalUpdate(metrics model.StatsMetrics) error {
	s.totalMu.Lock()
	defer s.totalMu.Unlock()
	if s.total.ID == 0 {
		s.total.ID = 1
	}
	s.total.Add(metrics)
	return nil
}

func StatsChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	return statsService.ChannelUpdate(channelID, metrics)
}

func (s *StatsService) ChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	mu := statsLockFor(&s.channelUpdateLocks, channelID)
	mu.Lock()
	defer mu.Unlock()

	channelCache, ok := s.channels.Get(channelID)
	if !ok {
		channelCache = model.StatsChannel{
			ChannelID: channelID,
		}
	}
	channelCache.Add(metrics)
	s.channels.Set(channelID, channelCache)
	s.markDirtyChannel(channelID)
	return nil
}

// StatsChannelKeyUpdate accumulates metrics for one credential. The channel ID
// is kept alongside the key ID so backups and migration validation can verify
// ownership instead of treating a credential counter as an unscoped integer.
func StatsChannelKeyUpdate(channelID, channelKeyID int, metrics model.StatsMetrics) error {
	return statsService.ChannelKeyUpdate(channelID, channelKeyID, metrics)
}

func (s *StatsService) ChannelKeyUpdate(channelID, channelKeyID int, metrics model.StatsMetrics) error {
	if channelID <= 0 || channelKeyID <= 0 {
		return fmt.Errorf("invalid channel key stats identity: channel_id=%d channel_key_id=%d", channelID, channelKeyID)
	}

	mu := statsLockFor(&s.channelKeyUpdateLocks, channelKeyID)
	mu.Lock()
	defer mu.Unlock()
	if _, deleted := s.deletedChannelKeys.Get(channelKeyID); deleted {
		return fmt.Errorf("%w: channel key was deleted while the request was in flight", ErrNotFound)
	}

	stats, ok := s.channelKeys.Get(channelKeyID)
	if !ok {
		stats = model.StatsChannelKey{ChannelID: channelID, ChannelKeyID: channelKeyID}
	} else if stats.ChannelID != 0 && stats.ChannelID != channelID {
		return fmt.Errorf("channel key stats ownership mismatch: key %d belongs to channel %d, got channel %d", channelKeyID, stats.ChannelID, channelID)
	}
	stats.ChannelID = channelID
	stats.Add(metrics)
	s.channelKeys.Set(channelKeyID, stats)
	s.dirtyChannelKeys.mark(channelKeyID)
	return nil
}

func StatsHourlyUpdate(metrics model.StatsMetrics) error {
	return statsService.HourlyUpdate(metrics)
}

func (s *StatsService) HourlyUpdate(metrics model.StatsMetrics) error {
	now := time.Now()
	nowHour := now.Hour()
	// hour 与 date 必须从同一次取时派生，跨午夜瞬间两次 time.Now() 会把 23 点数据记到新日期。
	todayDate := now.Format("20060102")

	s.hourlyMu.Lock()
	defer s.hourlyMu.Unlock()

	if s.hourly[nowHour].Date != todayDate {
		s.hourly[nowHour] = model.StatsHourly{
			Hour: nowHour,
			Date: todayDate,
		}
	}

	s.hourly[nowHour].Add(metrics)
	return nil
}

func StatsAPIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	return statsService.APIKeyUpdate(apiKeyID, metrics)
}

func (s *StatsService) APIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	mu := statsLockFor(&s.apiKeyUpdateLocks, apiKeyID)
	mu.Lock()
	defer mu.Unlock()
	if s.apiKeyDeletedLocked(apiKeyID) {
		return fmt.Errorf("%w: API key was deleted while the request was in flight", ErrNotFound)
	}

	apiKeyCache, ok := s.apiKeys.Get(apiKeyID)
	if !ok {
		apiKeyCache = model.StatsAPIKey{
			APIKeyID: apiKeyID,
		}
	}
	apiKeyCache.Add(metrics)
	s.apiKeys.Set(apiKeyID, apiKeyCache)
	s.markDirtyAPIKey(apiKeyID)
	return nil
}

func StatsChannelDel(id int) error {
	return statsService.ChannelDel(id)
}

func (s *StatsService) ChannelDel(id int) error {
	mu := statsLockFor(&s.channelUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()

	if _, ok := s.channels.Get(id); !ok {
		return nil
	}
	s.channels.Del(id)
	s.dirtyChannels.delete(id)
	return db.GetDB().Delete(&model.StatsChannel{}, id).Error
}

func StatsChannelKeyDel(id int) error {
	return statsService.ChannelKeyDel(id)
}

// StatsChannelKeyRestore clears a deletion tombstone when a credential ID is
// loaded into a new cache generation (for example after an import or a key
// creation on a database that reuses numeric IDs).
func StatsChannelKeyRestore(id int) {
	statsService.ChannelKeyRestore(id)
}

func (s *StatsService) ChannelKeyRestore(id int) {
	if id <= 0 {
		return
	}
	mu := statsLockFor(&s.channelKeyUpdateLocks, id)
	mu.Lock()
	s.deletedChannelKeys.Del(id)
	mu.Unlock()
}

func (s *StatsService) ChannelKeyDel(id int) error {
	if id <= 0 {
		return nil
	}
	mu := statsLockFor(&s.channelKeyUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()

	// Mark the identity before removing its row. A relay attempt that started
	// before deletion can finish after the transaction and must not recreate an
	// orphan stats row for a credential that no longer exists.
	s.deletedChannelKeys.Set(id, struct{}{})
	s.channelKeys.Del(id)
	s.dirtyChannelKeys.delete(id)
	if err := db.GetDB().Where("channel_key_id = ?", id).Delete(&model.StatsChannelKey{}).Error; err != nil {
		// Keep the in-memory tombstone only for a successful deletion. If the
		// database operation failed, a later retry must still be able to persist
		// the existing credential's stats.
		s.deletedChannelKeys.Del(id)
		return err
	}
	return nil
}
