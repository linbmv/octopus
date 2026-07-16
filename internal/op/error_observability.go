package op

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

const (
	StatsErrorLevelsDefaultWindowHours = 24
	StatsErrorLevelsMaxWindowHours     = 7 * 24
	StatsErrorLevelsScanCapacity       = 10_000
	errorLevelTrendBucketSeconds       = int64(time.Hour / time.Second)
)

// StatsErrorLevelsGet aggregates classified attempts from both the bounded
// in-memory RelayLog cache and persisted RelayLogs. The newest logs win when
// the capacity is reached, so request latency and memory remain deterministic.
func StatsErrorLevelsGet(ctx context.Context, windowHours, channelID int) (model.StatsErrorLevels, error) {
	return relayLogService.ErrorLevels(ctx, windowHours, channelID, time.Now())
}

func (s *RelayLogService) ErrorLevels(
	ctx context.Context,
	windowHours int,
	channelID int,
	now time.Time,
) (model.StatsErrorLevels, error) {
	return s.errorLevelsWithCapacity(ctx, windowHours, channelID, now, StatsErrorLevelsScanCapacity)
}

func (s *RelayLogService) errorLevelsWithCapacity(
	ctx context.Context,
	windowHours int,
	channelID int,
	now time.Time,
	capacity int,
) (model.StatsErrorLevels, error) {
	if windowHours < 1 || windowHours > StatsErrorLevelsMaxWindowHours {
		return model.StatsErrorLevels{}, fmt.Errorf("window_hours must be between 1 and %d", StatsErrorLevelsMaxWindowHours)
	}
	if channelID < 0 {
		return model.StatsErrorLevels{}, fmt.Errorf("channel_id must be non-negative")
	}

	to := now.Unix()
	from := now.Add(-time.Duration(windowHours) * time.Hour).Unix()
	if capacity < 1 || capacity > StatsErrorLevelsScanCapacity {
		return model.StatsErrorLevels{}, fmt.Errorf("capacity must be between 1 and %d", StatsErrorLevelsScanCapacity)
	}
	logs, truncated, err := s.errorLevelSourceLogs(ctx, from, to, capacity)
	if err != nil {
		return model.StatsErrorLevels{}, err
	}

	result := model.StatsErrorLevels{
		From:        from,
		To:          to,
		WindowHours: windowHours,
		ChannelID:   channelID,
		ScannedLogs: len(logs),
		Capacity:    capacity,
		Truncated:   truncated,
		Trend:       make([]model.StatsErrorLevelTrendPoint, 0),
	}
	buckets := make(map[int64]*model.StatsErrorLevelTrendPoint)
	for i := range logs {
		entry := &logs[i]
		bucketStart := entry.Time - entry.Time%errorLevelTrendBucketSeconds
		for j := range entry.Attempts {
			attempt := &entry.Attempts[j]
			if attempt.Status != model.AttemptFailed || (channelID > 0 && attempt.ChannelID != channelID) {
				continue
			}
			if !addErrorLevelCount(&result.Counts, attempt.ErrorLevel) {
				continue
			}
			point := buckets[bucketStart]
			if point == nil {
				point = &model.StatsErrorLevelTrendPoint{BucketStart: bucketStart}
				buckets[bucketStart] = point
			}
			addErrorLevelCount(&point.StatsErrorLevelCounts, attempt.ErrorLevel)
		}
	}

	for _, point := range buckets {
		result.Trend = append(result.Trend, *point)
	}
	sort.Slice(result.Trend, func(i, j int) bool {
		return result.Trend[i].BucketStart < result.Trend[j].BucketStart
	})
	return result, nil
}

func addErrorLevelCount(counts *model.StatsErrorLevelCounts, level model.AttemptErrorLevel) bool {
	switch level {
	case model.AttemptErrorLevelKey:
		counts.Key++
	case model.AttemptErrorLevelChannel:
		counts.Channel++
	case model.AttemptErrorLevelClient:
		counts.Client++
	default:
		return false
	}
	return true
}

func (s *RelayLogService) errorLevelSourceLogs(ctx context.Context, from, to int64, capacity int) ([]model.RelayLog, bool, error) {
	if capacity < 1 {
		return nil, false, nil
	}

	s.cacheMu.Lock()
	cached := make([]model.RelayLog, 0, len(s.cache))
	for i := range s.cache {
		if s.cache[i].Time >= from && s.cache[i].Time <= to {
			cached = append(cached, s.cache[i])
		}
	}
	s.cacheMu.Unlock()

	// Fetch enough extra persisted rows to compensate for cache/DB overlap
	// during an asynchronous flush, plus one sentinel to prove truncation.
	var persisted []model.RelayLog
	err := db.GetDB().WithContext(ctx).
		// Error observability needs only the compact attempt JSON. Selecting full
		// request/response bodies for up to 10k logs would keep the query bounded
		// in row count but still create avoidable hundreds-of-megabytes pressure.
		Select("id", "time", "attempts").
		Where("time >= ? AND time <= ?", from, to).
		Order("time DESC").
		Order("id DESC").
		Limit(capacity + len(cached) + 1).
		Find(&persisted).Error
	if err != nil {
		return nil, false, err
	}

	byID := make(map[int64]model.RelayLog, len(cached)+len(persisted))
	for i := range persisted {
		byID[persisted[i].ID] = persisted[i]
	}
	for i := range cached {
		byID[cached[i].ID] = cached[i]
	}
	merged := make([]model.RelayLog, 0, len(byID))
	for _, entry := range byID {
		merged = append(merged, entry)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Time == merged[j].Time {
			return merged[i].ID > merged[j].ID
		}
		return merged[i].Time > merged[j].Time
	})

	truncated := len(merged) > capacity
	if truncated {
		merged = merged[:capacity]
	}
	return merged, truncated, nil
}
