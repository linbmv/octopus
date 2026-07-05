package op

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var statsService = NewStatsService()

type StatsService struct {
	daily   model.StatsDaily
	dailyMu sync.RWMutex

	total   model.StatsTotal
	totalMu sync.RWMutex

	hourly   [24]model.StatsHourly
	hourlyMu sync.RWMutex

	channels        cache.Cache[int, model.StatsChannel]
	dirtyChannels   map[int]struct{}
	dirtyChannelsMu sync.Mutex

	models        cache.Cache[int, model.StatsModel]
	dirtyModels   map[int]struct{}
	dirtyModelsMu sync.Mutex

	apiKeys        cache.Cache[int, model.StatsAPIKey]
	dirtyAPIKeys   map[int]struct{}
	dirtyAPIKeysMu sync.Mutex

	saveSignal chan model.StatsDaily

	workerMu   sync.Mutex
	workerStop chan struct{}
	workerDone chan struct{}
}

func NewStatsService() *StatsService {
	return &StatsService{
		channels:      cache.New[int, model.StatsChannel](16),
		dirtyChannels: make(map[int]struct{}),
		models:        cache.New[int, model.StatsModel](16),
		dirtyModels:   make(map[int]struct{}),
		apiKeys:       cache.New[int, model.StatsAPIKey](16),
		dirtyAPIKeys:  make(map[int]struct{}),
		saveSignal:    make(chan model.StatsDaily, 16),
	}
}

func DefaultStatsService() *StatsService {
	return statsService
}

func (s *StatsService) totalSnapshot() model.StatsTotal {
	s.totalMu.RLock()
	defer s.totalMu.RUnlock()
	return s.total
}

func (s *StatsService) dailySnapshot() model.StatsDaily {
	s.dailyMu.RLock()
	defer s.dailyMu.RUnlock()
	return s.daily
}

func (s *StatsService) hourlySnapshot() [24]model.StatsHourly {
	s.hourlyMu.RLock()
	defer s.hourlyMu.RUnlock()
	return s.hourly
}

func (s *StatsService) markDirtyChannel(id int) {
	s.dirtyChannelsMu.Lock()
	s.dirtyChannels[id] = struct{}{}
	s.dirtyChannelsMu.Unlock()
}

func (s *StatsService) markDirtyModel(id int) {
	s.dirtyModelsMu.Lock()
	s.dirtyModels[id] = struct{}{}
	s.dirtyModelsMu.Unlock()
}

func (s *StatsService) markDirtyAPIKey(id int) {
	s.dirtyAPIKeysMu.Lock()
	s.dirtyAPIKeys[id] = struct{}{}
	s.dirtyAPIKeysMu.Unlock()
}

func (s *StatsService) takeDirtyChannels() []int {
	s.dirtyChannelsMu.Lock()
	defer s.dirtyChannelsMu.Unlock()

	ids := make([]int, 0, len(s.dirtyChannels))
	for id := range s.dirtyChannels {
		ids = append(ids, id)
	}
	s.dirtyChannels = make(map[int]struct{})
	return ids
}

func (s *StatsService) takeDirtyModels() []int {
	s.dirtyModelsMu.Lock()
	defer s.dirtyModelsMu.Unlock()

	ids := make([]int, 0, len(s.dirtyModels))
	for id := range s.dirtyModels {
		ids = append(ids, id)
	}
	s.dirtyModels = make(map[int]struct{})
	return ids
}

func (s *StatsService) takeDirtyAPIKeys() []int {
	s.dirtyAPIKeysMu.Lock()
	defer s.dirtyAPIKeysMu.Unlock()

	ids := make([]int, 0, len(s.dirtyAPIKeys))
	for id := range s.dirtyAPIKeys {
		ids = append(ids, id)
	}
	s.dirtyAPIKeys = make(map[int]struct{})
	return ids
}

func StatsSaveDBTask() {
	statsService.SaveDBTask()
}

func (s *StatsService) SaveDBTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log.Debugf("stats save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("stats save db task finished, save time: %s", time.Since(startTime))
	}()
	if err := s.SaveDB(ctx); err != nil {
		log.Errorf("stats save db error: %v", err)
		return
	}
}

func StatsSaveDB(ctx context.Context) error {
	return statsService.SaveDB(ctx)
}

func (s *StatsService) SaveDB(ctx context.Context) error {
	totalSnap := s.totalSnapshot()
	if totalSnap.ID == 0 {
		totalSnap.ID = 1
	}

	dailySnap := s.dailySnapshot()
	hourlyAll := s.hourlySnapshot()

	channelIDs := s.takeDirtyChannels()
	modelIDs := s.takeDirtyModels()
	apiKeyIDs := s.takeDirtyAPIKeys()

	return s.persistSnapshots(ctx, totalSnap, dailySnap, hourlyAll, channelIDs, modelIDs, apiKeyIDs)
}

func (s *StatsService) persistSnapshots(
	ctx context.Context,
	totalSnap model.StatsTotal,
	dailySnap model.StatsDaily,
	hourlyAll [24]model.StatsHourly,
	channelIDs []int,
	modelIDs []int,
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

	for _, id := range modelIDs {
		m, ok := s.models.Get(id)
		if !ok {
			continue
		}
		if result := dbConn.Save(&m); result.Error != nil {
			return result.Error
		}
	}

	for _, id := range apiKeyIDs {
		ak, ok := s.apiKeys.Get(id)
		if !ok {
			continue
		}
		if result := dbConn.Save(&ak); result.Error != nil {
			return result.Error
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

	channelIDs := s.takeDirtyChannels()
	modelIDs := s.takeDirtyModels()
	apiKeyIDs := s.takeDirtyAPIKeys()

	return s.persistSnapshots(ctx, totalSnap, dailyOverride, hourlyAll, channelIDs, modelIDs, apiKeyIDs)
}

func (s *StatsService) signalSave(daily model.StatsDaily) {
	if daily.Date == "" {
		return
	}

	select {
	case s.saveSignal <- daily:
	default:
		log.Warnf("stats async save queue full, dropping daily snapshot: date=%s", daily.Date)
	}
}

func startStatsSaveWorker() {
	statsService.StartSaveWorker()
}

func (s *StatsService) StartSaveWorker() {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerStop != nil {
		return
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.workerStop = stopCh
	s.workerDone = doneCh
	go s.saveWorker(stopCh, doneCh)
}

func stopStatsSaveWorker(ctx context.Context) error {
	return statsService.StopSaveWorker(ctx)
}

func (s *StatsService) StopSaveWorker(ctx context.Context) error {
	s.workerMu.Lock()
	stopCh := s.workerStop
	doneCh := s.workerDone
	if stopCh == nil || doneCh == nil {
		s.workerMu.Unlock()
		return nil
	}
	s.workerStop = nil
	s.workerDone = nil
	close(stopCh)
	s.workerMu.Unlock()

	select {
	case <-doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *StatsService) saveWorker(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	for {
		select {
		case daily := <-s.saveSignal:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := s.saveDBWithDailyOverride(ctx, daily); err != nil {
				log.Errorf("stats async save error: %v", err)
			}
			cancel()
		case <-stopCh:
			return
		}
	}
}

func StatsDailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	return statsService.DailyUpdate(ctx, metrics)
}

func (s *StatsService) DailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	today := time.Now().Format("20060102")

	s.dailyMu.Lock()
	if s.daily.Date == today {
		s.daily.StatsMetrics.Add(metrics)
		s.dailyMu.Unlock()
		return nil
	}

	prevDaily := s.daily
	s.daily = model.StatsDaily{Date: today}
	s.daily.StatsMetrics.Add(metrics)
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
	s.total.StatsMetrics.Add(metrics)
	return nil
}

func StatsChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	return statsService.ChannelUpdate(channelID, metrics)
}

func (s *StatsService) ChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	channelCache, ok := s.channels.Get(channelID)
	if !ok {
		channelCache = model.StatsChannel{
			ChannelID: channelID,
		}
	}
	channelCache.StatsMetrics.Add(metrics)
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
	todayDate := time.Now().Format("20060102")

	s.hourlyMu.Lock()
	defer s.hourlyMu.Unlock()

	if s.hourly[nowHour].Date != todayDate {
		s.hourly[nowHour] = model.StatsHourly{
			Hour: nowHour,
			Date: todayDate,
		}
	}

	s.hourly[nowHour].StatsMetrics.Add(metrics)
	return nil
}

func StatsModelUpdate(stats model.StatsModel) error {
	return statsService.ModelUpdate(stats)
}

func (s *StatsService) ModelUpdate(stats model.StatsModel) error {
	modelCache, ok := s.models.Get(stats.ID)
	if !ok {
		modelCache = model.StatsModel{
			ID: stats.ID,
		}
	}
	modelCache.StatsMetrics.Add(stats.StatsMetrics)
	s.models.Set(stats.ID, modelCache)
	s.markDirtyModel(stats.ID)
	return nil
}

func StatsAPIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	return statsService.APIKeyUpdate(apiKeyID, metrics)
}

func (s *StatsService) APIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	apiKeyCache, ok := s.apiKeys.Get(apiKeyID)
	if !ok {
		apiKeyCache = model.StatsAPIKey{
			APIKeyID: apiKeyID,
		}
	}
	apiKeyCache.StatsMetrics.Add(metrics)
	s.apiKeys.Set(apiKeyID, apiKeyCache)
	s.markDirtyAPIKey(apiKeyID)
	return nil
}

func StatsChannelDel(id int) error {
	return statsService.ChannelDel(id)
}

func (s *StatsService) ChannelDel(id int) error {
	if _, ok := s.channels.Get(id); !ok {
		return nil
	}
	s.channels.Del(id)
	s.dirtyChannelsMu.Lock()
	delete(s.dirtyChannels, id)
	s.dirtyChannelsMu.Unlock()
	return db.GetDB().Delete(&model.StatsChannel{}, id).Error
}

func StatsAPIKeyDel(id int) error {
	return statsService.APIKeyDel(id)
}

func (s *StatsService) APIKeyDel(id int) error {
	if _, ok := s.apiKeys.Get(id); !ok {
		return nil
	}
	s.apiKeys.Del(id)
	s.dirtyAPIKeysMu.Lock()
	delete(s.dirtyAPIKeys, id)
	s.dirtyAPIKeysMu.Unlock()
	return db.GetDB().Delete(&model.StatsAPIKey{}, id).Error
}

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

func StatsAPIKeyGet(id int) model.StatsAPIKey {
	return statsService.APIKeyGet(id)
}

func (s *StatsService) APIKeyGet(id int) model.StatsAPIKey {
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
	return apiKeys
}

func StatsHourlyGet() []model.StatsHourly {
	return statsService.HourlyGet()
}

func (s *StatsService) HourlyGet() []model.StatsHourly {
	now := time.Now()
	currentHour := now.Hour()
	todayDate := time.Now().Format("20060102")

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
	s.dirtyChannelsMu.Lock()
	s.dirtyChannels = make(map[int]struct{})
	s.dirtyChannelsMu.Unlock()
	for _, v := range loadedChannels {
		s.channels.Set(v.ChannelID, v)
	}

	var loadedAPIKeys []model.StatsAPIKey
	result = dbConn.Find(&loadedAPIKeys)
	if result.Error != nil {
		return fmt.Errorf("failed to get api key stats: %v", result.Error)
	}

	s.apiKeys.Clear()
	s.dirtyAPIKeysMu.Lock()
	s.dirtyAPIKeys = make(map[int]struct{})
	s.dirtyAPIKeysMu.Unlock()
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
