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
