package op

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func StatsTotalGet() model.StatsTotal {
	return statsService.TotalGet()
}

func (s *StatsService) TotalGet() model.StatsTotal {
	return s.totalSnapshot()
}

func StatsTodayGet() model.StatsDaily {
	return statsService.TodayGet()
}

func (s *StatsService) TodayGet() model.StatsDaily {
	return s.dailySnapshot()
}

func StatsChannelGet(id int) model.StatsChannel {
	return statsService.ChannelGet(id)
}

func (s *StatsService) ChannelGet(id int) model.StatsChannel {
	mu := statsLockFor(&s.channelUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()

	stats, ok := s.channels.Get(id)
	if !ok {
		tmp := model.StatsChannel{
			ChannelID: id,
		}
		s.channels.Set(id, tmp)
		s.markDirtyChannel(id)
		return tmp
	}
	return stats
}

func StatsChannelKeyGet(id int) model.StatsChannelKey {
	return statsService.ChannelKeyGet(id)
}

func (s *StatsService) ChannelKeyGet(id int) model.StatsChannelKey {
	if id <= 0 {
		return model.StatsChannelKey{}
	}
	mu := statsLockFor(&s.channelKeyUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()

	if stats, ok := s.channelKeys.Get(id); ok {
		return stats
	}
	// A newly-created key has no persisted stats row until it is actually used.
	// Returning an identity-only value keeps list reads side-effect free while
	// still allowing the API to render zero counters immediately.
	return model.StatsChannelKey{ChannelKeyID: id}
}

func StatsAPIKeyGet(id int) model.StatsAPIKey {
	return statsService.APIKeyGet(id)
}

func (s *StatsService) APIKeyGet(id int) model.StatsAPIKey {
	mu := statsLockFor(&s.apiKeyUpdateLocks, id)
	mu.Lock()
	defer mu.Unlock()
	if s.apiKeyDeletedLocked(id) {
		// A request that authenticated before deletion may still render an
		// error/status response. Do not recreate a dirty stats row while its
		// reservation keeps the deletion tombstone alive.
		return model.StatsAPIKey{APIKeyID: id}
	}

	stats, ok := s.apiKeys.Get(id)
	if !ok {
		tmp := model.StatsAPIKey{
			APIKeyID: id,
		}
		s.apiKeys.Set(id, tmp)
		s.markDirtyAPIKey(id)
		return tmp
	}
	return stats
}

func StatsAPIKeyList() []model.StatsAPIKey {
	return statsService.APIKeyList()
}

func (s *StatsService) APIKeyList() []model.StatsAPIKey {
	apiKeys := make([]model.StatsAPIKey, 0, s.apiKeys.Len())
	for _, v := range s.apiKeys.GetAll() {
		apiKeys = append(apiKeys, v)
	}
	sort.Slice(apiKeys, func(i, j int) bool { return apiKeys[i].APIKeyID < apiKeys[j].APIKeyID })
	return apiKeys
}

func StatsHourlyGet() []model.StatsHourly {
	return statsService.HourlyGet()
}

func (s *StatsService) HourlyGet() []model.StatsHourly {
	now := time.Now()
	currentHour := now.Hour()
	todayDate := now.Format("20060102")

	s.hourlyMu.RLock()
	defer s.hourlyMu.RUnlock()

	result := make([]model.StatsHourly, 0, currentHour+1)

	for hour := 0; hour <= currentHour; hour++ {
		if s.hourly[hour].Date == todayDate {
			result = append(result, s.hourly[hour])
		} else {
			result = append(result, model.StatsHourly{
				Hour: hour,
				Date: todayDate,
			})
		}
	}

	return result
}

func StatsGetDaily(ctx context.Context) ([]model.StatsDaily, error) {
	return statsService.GetDaily(ctx)
}

func (s *StatsService) GetDaily(ctx context.Context) ([]model.StatsDaily, error) {
	var statsDaily []model.StatsDaily
	result := db.GetDB().WithContext(ctx).Find(&statsDaily)
	if result.Error != nil {
		return nil, result.Error
	}
	return statsDaily, nil
}

func statsRefreshCache(ctx context.Context) error {
	return statsService.RefreshCache(ctx)
}

func (s *StatsService) RefreshCache(ctx context.Context) error {
	dbConn := db.GetDB().WithContext(ctx)
	today := time.Now().Format("20060102")

	var loadedDaily model.StatsDaily
	result := dbConn.Last(&loadedDaily)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get daily stats: %v", result.Error)
	}
	if result.RowsAffected == 0 || loadedDaily.Date != today {
		loadedDaily = model.StatsDaily{Date: today}
	}

	var loadedTotal model.StatsTotal
	result = dbConn.First(&loadedTotal)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get total stats: %v", result.Error)
	}
	if result.RowsAffected == 0 {
		loadedTotal = model.StatsTotal{ID: 1}
	} else if loadedTotal.ID == 0 {
		loadedTotal.ID = 1
	}

	var loadedChannels []model.StatsChannel
	result = dbConn.Find(&loadedChannels)
	if result.Error != nil {
		return fmt.Errorf("failed to get channels: %v", result.Error)
	}

	var loadedHourly []model.StatsHourly
	result = dbConn.Find(&loadedHourly)
	if result.Error != nil {
		return fmt.Errorf("failed to get hourly stats: %v", result.Error)
	}

	s.dailyMu.Lock()
	s.daily = loadedDaily
	s.dailyMu.Unlock()

	s.totalMu.Lock()
	s.total = loadedTotal
	s.totalMu.Unlock()

	s.channels.Clear()
	s.dirtyChannels.reset()
	for _, v := range loadedChannels {
		s.channels.Set(v.ChannelID, v)
	}

	var loadedChannelKeys []model.StatsChannelKey
	result = dbConn.Find(&loadedChannelKeys)
	if result.Error != nil {
		return fmt.Errorf("failed to get channel key stats: %v", result.Error)
	}

	s.channelKeys.Clear()
	// A full cache refresh follows restore/import and may legitimately reuse a
	// numeric credential ID. Tombstones only protect the current in-memory
	// generation, so discard them before loading the authoritative rows.
	s.deletedChannelKeys.Clear()
	s.dirtyChannelKeys.reset()
	for _, v := range loadedChannelKeys {
		s.channelKeys.Set(v.ChannelKeyID, v)
	}

	var loadedAPIKeys []model.StatsAPIKey
	result = dbConn.Find(&loadedAPIKeys)
	if result.Error != nil {
		return fmt.Errorf("failed to get api key stats: %v", result.Error)
	}

	s.apiKeys.Clear()
	s.dirtyAPIKeys.reset()
	for _, v := range loadedAPIKeys {
		s.apiKeys.Set(v.APIKeyID, v)
	}

	s.hourlyMu.Lock()
	s.hourly = [24]model.StatsHourly{}
	for _, v := range loadedHourly {
		if v.Hour >= 0 && v.Hour < 24 {
			s.hourly[v.Hour] = v
		}
	}
	s.hourlyMu.Unlock()

	return nil
}
