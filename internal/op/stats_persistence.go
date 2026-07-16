package op

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm/clause"
)

func (s *StatsService) SaveDBTask() {
	if err := s.SaveDBTaskContext(context.Background()); err != nil {
		log.Errorf("stats save db error: %v", err)
	}
}

func StatsSaveDBTaskContext(parent context.Context) error {
	return statsService.SaveDBTaskContext(parent)
}

func (s *StatsService) SaveDBTaskContext(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	log.Debugf("stats save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("stats save db task finished, save time: %s", time.Since(startTime))
	}()
	if err := s.SaveDB(ctx); err != nil {
		return err
	}
	return nil
}

func StatsSaveDB(ctx context.Context) error {
	return statsService.SaveDB(ctx)
}

func (s *StatsService) SaveDB(ctx context.Context) error {
	for _, daily := range s.drainPendingDailySnapshots() {
		if err := s.saveDBWithDailyOverride(ctx, daily); err != nil {
			return err
		}
		s.ackPendingDaily(daily)
	}

	return s.saveDBWithDailyOverride(ctx, s.dailySnapshot())
}

func (s *StatsService) persistSnapshots(
	ctx context.Context,
	totalSnap model.StatsTotal,
	dailySnap model.StatsDaily,
	hourlyAll [24]model.StatsHourly,
	channelIDs []int,
	apiKeyIDs []int,
) error {
	dbConn := db.GetDB().WithContext(ctx)

	if result := dbConn.Save(&totalSnap); result.Error != nil {
		return result.Error
	}
	if result := dbConn.Save(&dailySnap); result.Error != nil {
		return result.Error
	}

	todayDate := time.Now().Format("20060102")
	hourlyStats := make([]model.StatsHourly, 0, 24)
	for hour := 0; hour < 24; hour++ {
		if hourlyAll[hour].Date == todayDate {
			hourlyStats = append(hourlyStats, hourlyAll[hour])
		}
	}
	if len(hourlyStats) > 0 {
		if result := dbConn.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "hour"}},
			UpdateAll: true,
		}).Create(&hourlyStats); result.Error != nil {
			return result.Error
		}
	}

	for _, id := range channelIDs {
		ch, ok := s.channels.Get(id)
		if !ok {
			continue
		}
		if result := dbConn.Save(&ch); result.Error != nil {
			return result.Error
		}
	}

	for _, id := range apiKeyIDs {
		if err := func() error {
			mu := statsLockFor(&s.apiKeyUpdateLocks, id)
			mu.Lock()
			defer mu.Unlock()

			if s.apiKeyDeletedLocked(id) {
				return nil
			}
			ak, ok := s.apiKeys.Get(id)
			if !ok {
				return nil
			}
			return dbConn.Save(&ak).Error
		}(); err != nil {
			return err
		}
	}

	return nil
}

func (s *StatsService) saveDBWithDailyOverride(ctx context.Context, dailyOverride model.StatsDaily) error {
	totalSnap := s.totalSnapshot()
	if totalSnap.ID == 0 {
		totalSnap.ID = 1
	}

	hourlyAll := s.hourlySnapshot()

	channelDirty := s.dirtyChannels.snapshot()
	apiKeyDirty := s.dirtyAPIKeys.snapshot()

	err := s.persistSnapshots(
		ctx,
		totalSnap,
		dailyOverride,
		hourlyAll,
		dirtyIDs(channelDirty),
		dirtyIDs(apiKeyDirty),
	)
	if err != nil {
		return err
	}

	s.dirtyChannels.clearUnchanged(channelDirty)
	s.dirtyAPIKeys.clearUnchanged(apiKeyDirty)
	return nil
}

func (s *StatsService) signalSave(daily model.StatsDaily) {
	if daily.Date == "" {
		return
	}

	s.pendingDailyMu.Lock()
	s.pendingDaily[daily.Date] = daily
	s.pendingDailyMu.Unlock()

	select {
	case s.pendingDailyNotify <- struct{}{}:
	default:
	}
}

func (s *StatsService) drainPendingDailySnapshots() []model.StatsDaily {
	s.pendingDailyMu.Lock()
	defer s.pendingDailyMu.Unlock()

	snapshots := make([]model.StatsDaily, 0, len(s.pendingDaily))
	for _, daily := range s.pendingDaily {
		snapshots = append(snapshots, daily)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Date < snapshots[j].Date
	})
	return snapshots

}

func (s *StatsService) ackPendingDaily(daily model.StatsDaily) {
	s.pendingDailyMu.Lock()
	defer s.pendingDailyMu.Unlock()

	if current, ok := s.pendingDaily[daily.Date]; ok && current == daily {
		delete(s.pendingDaily, daily.Date)
	}
}

func (s *StatsService) StartSaveWorker() {
	_ = s.StartSaveWorkerContext(context.Background())
}

func (s *StatsService) StartSaveWorkerContext(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		select {
		case <-s.workerDone:
			s.workerCancel = nil
			s.workerDone = nil
		default:
			return errors.New("stats save worker is still running")
		}
	}

	ctx, cancel := context.WithCancel(parent)
	doneCh := make(chan struct{})
	s.workerCancel = cancel
	s.workerDone = doneCh
	go s.saveWorker(ctx, doneCh)
	return nil
}

func (s *StatsService) StopSaveWorker(ctx context.Context) error {
	s.workerMu.Lock()
	cancel := s.workerCancel
	doneCh := s.workerDone
	if cancel == nil || doneCh == nil {
		s.workerMu.Unlock()
		return nil
	}
	cancel()
	s.workerMu.Unlock()

	select {
	case <-doneCh:
		s.workerMu.Lock()
		if s.workerDone == doneCh {
			s.workerCancel = nil
			s.workerDone = nil
		}
		s.workerMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *StatsService) saveWorker(ctx context.Context, doneCh chan<- struct{}) {
	defer close(doneCh)
	for {
		select {
		case <-s.pendingDailyNotify:
			for _, daily := range s.drainPendingDailySnapshots() {
				flushCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				if err := s.saveDBWithDailyOverride(flushCtx, daily); err != nil {
					if ctx.Err() == nil {
						s.saveFailures.Add(1)
						log.Errorf("stats async save error: %v", err)
					}
					cancel()
					continue
				}
				cancel()
				s.ackPendingDaily(daily)
			}
		case <-ctx.Done():
			return
		}
	}
}
