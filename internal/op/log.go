package op

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/snowflake"
)

const relayLogMaxSize = 20
const relayLogMaxSizeNoDB = 100 // 当不保存到数据库时，允许更大的缓存用于实时查询

var relayLogService = NewRelayLogService()

type RelayLogService struct {
	cache   []model.RelayLog
	cacheMu sync.Mutex

	flushMu     sync.Mutex
	flushSignal chan struct{}

	workerMu   sync.Mutex
	workerStop chan struct{}
	workerDone chan struct{}

	subscribers   map[chan model.RelayLog]struct{}
	subscribersMu sync.RWMutex

	streamTokens   map[string]struct{}
	streamTokensMu sync.RWMutex
}

func NewRelayLogService() *RelayLogService {
	return &RelayLogService{
		cache:        make([]model.RelayLog, 0, relayLogMaxSize),
		flushSignal:  make(chan struct{}, 1),
		subscribers:  make(map[chan model.RelayLog]struct{}),
		streamTokens: make(map[string]struct{}),
	}
}

func RelayLogStreamTokenCreate() (string, error) {
	return relayLogService.StreamTokenCreate()
}

func (s *RelayLogService) StreamTokenCreate() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)

	s.streamTokensMu.Lock()
	s.streamTokens[token] = struct{}{}
	s.streamTokensMu.Unlock()

	return token, nil
}

func RelayLogStreamTokenVerify(token string) bool {
	return relayLogService.StreamTokenVerify(token)
}

func (s *RelayLogService) StreamTokenVerify(token string) bool {
	s.streamTokensMu.RLock()
	defer s.streamTokensMu.RUnlock()
	_, ok := s.streamTokens[token]
	return ok
}

func RelayLogStreamTokenRevoke(token string) {
	relayLogService.StreamTokenRevoke(token)
}

func (s *RelayLogService) StreamTokenRevoke(token string) {
	s.streamTokensMu.Lock()
	delete(s.streamTokens, token)
	s.streamTokensMu.Unlock()
}

func RelayLogSubscribe() chan model.RelayLog {
	return relayLogService.Subscribe()
}

func (s *RelayLogService) Subscribe() chan model.RelayLog {
	ch := make(chan model.RelayLog, 10)
	s.subscribersMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subscribersMu.Unlock()
	return ch
}

func RelayLogUnsubscribe(ch chan model.RelayLog) {
	relayLogService.Unsubscribe(ch)
}

func (s *RelayLogService) Unsubscribe(ch chan model.RelayLog) {
	s.subscribersMu.Lock()
	delete(s.subscribers, ch)
	s.subscribersMu.Unlock()
	close(ch)
}

func (s *RelayLogService) notifySubscribers(relayLog model.RelayLog) {
	s.subscribersMu.RLock()
	defer s.subscribersMu.RUnlock()

	for ch := range s.subscribers {
		select {
		case ch <- relayLog:
		default:
		}
	}
}

func (s *RelayLogService) flushToDB(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.cacheMu.Lock()
	if len(s.cache) == 0 {
		s.cacheMu.Unlock()
		return nil
	}
	batch := make([]model.RelayLog, len(s.cache))
	copy(batch, s.cache)
	flushedUpto := len(batch)
	s.cacheMu.Unlock()

	result := db.GetDB().WithContext(ctx).Create(&batch)
	if result.Error != nil {
		return result.Error
	}

	s.cacheMu.Lock()
	if len(s.cache) >= flushedUpto {
		s.cache = s.cache[flushedUpto:]
	} else {
		s.cache = s.cache[:0]
	}
	if len(s.cache) == 0 {
		s.cache = make([]model.RelayLog, 0, relayLogMaxSize)
	}
	s.cacheMu.Unlock()

	return nil
}

func (s *RelayLogService) signalFlush() {
	select {
	case s.flushSignal <- struct{}{}:
	default:
	}
}

func startRelayLogFlushWorker() {
	relayLogService.StartFlushWorker()
}

func (s *RelayLogService) StartFlushWorker() {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerStop != nil {
		return
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.workerStop = stopCh
	s.workerDone = doneCh
	go s.flushWorker(stopCh, doneCh)
}

func stopRelayLogFlushWorker(ctx context.Context) error {
	return relayLogService.StopFlushWorker(ctx)
}

func (s *RelayLogService) StopFlushWorker(ctx context.Context) error {
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

func (s *RelayLogService) flushWorker(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	for {
		select {
		case <-s.flushSignal:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.flushToDB(ctx); err != nil {
				log.Errorf("relay log async flush error: %v", err)
			}
			cancel()
		case <-stopCh:
			return
		}
	}
}

func RelayLogAdd(ctx context.Context, relayLog model.RelayLog) error {
	return relayLogService.Add(ctx, relayLog)
}

func (s *RelayLogService) Add(ctx context.Context, relayLog model.RelayLog) error {
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}
	maxSize := relayLogMaxSize
	if !enabled {
		maxSize = relayLogMaxSizeNoDB
	}
	relayLog.ID = snowflake.GenerateID()
	go s.notifySubscribers(relayLog)

	s.cacheMu.Lock()
	s.cache = append(s.cache, relayLog)
	if len(s.cache) >= maxSize {
		if enabled {
			s.cacheMu.Unlock()
			s.signalFlush()
			return nil
		}
		// 如果未启用日志保存，移除最旧的日志，保留最新的日志用于实时查询
		// 重建底层数组而不是 reslice，避免数组持续引用旧日志的 Request/ResponseContent 导致内存无法回收
		keepSize := maxSize / 2
		if len(s.cache) > keepSize {
			newCache := make([]model.RelayLog, keepSize, maxSize)
			copy(newCache, s.cache[len(s.cache)-keepSize:])
			s.cache = newCache
		}
	}
	s.cacheMu.Unlock()
	return nil
}

func RelayLogSaveDBTask(ctx context.Context) error {
	return relayLogService.SaveDBTask(ctx)
}

func (s *RelayLogService) SaveDBTask(ctx context.Context) error {
	log.Debugf("relay log save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("relay log save db task finished, save time: %s", time.Since(startTime))
	}()
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}

	if enabled {
		if err := s.flushToDB(ctx); err != nil {
			return err
		}
		return s.cleanup(ctx)
	}

	// 如果未启用日志保存，检查缓存大小，如果超过限制则清理旧日志
	s.cacheMu.Lock()
	if len(s.cache) > relayLogMaxSizeNoDB {
		keepSize := relayLogMaxSizeNoDB / 2
		newCache := make([]model.RelayLog, keepSize, relayLogMaxSizeNoDB)
		copy(newCache, s.cache[len(s.cache)-keepSize:])
		s.cache = newCache
	}
	s.cacheMu.Unlock()

	return nil
}

func (s *RelayLogService) cleanup(ctx context.Context) error {
	keepPeriod, err := SettingGetInt(model.SettingKeyRelayLogKeepPeriod)
	if err != nil {
		return err
	}

	if keepPeriod <= 0 {
		return nil
	}

	cutoffTime := time.Now().Add(-time.Duration(keepPeriod) * 24 * time.Hour).Unix()
	return db.GetDB().WithContext(ctx).Where("time < ?", cutoffTime).Delete(&model.RelayLog{}).Error
}

// RelayLogList 查询日志列表，支持可选的时间范围过滤
// startTime 和 endTime 为 nil 时表示不限制时间范围
func RelayLogList(ctx context.Context, startTime, endTime *int, page, pageSize int) ([]model.RelayLog, error) {
	return relayLogService.List(ctx, startTime, endTime, page, pageSize)
}

func (s *RelayLogService) List(ctx context.Context, startTime, endTime *int, page, pageSize int) ([]model.RelayLog, error) {
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return nil, err
	}
	hasTimeFilter := startTime != nil && endTime != nil

	// 获取缓存中符合条件的日志
	s.cacheMu.Lock()
	var cachedLogs []model.RelayLog
	for _, log := range s.cache {
		if hasTimeFilter {
			if log.Time >= int64(*startTime) && log.Time <= int64(*endTime) {
				cachedLogs = append(cachedLogs, log)
			}
		} else {
			cachedLogs = append(cachedLogs, log)
		}
	}
	s.cacheMu.Unlock()

	// 反转缓存日志顺序（原本新的在末尾，反转后新的在前面，方便分页）
	sortRelayLogsNewestFirst(cachedLogs)

	cacheCount := len(cachedLogs)
	offset := (page - 1) * pageSize

	var result []model.RelayLog

	// 先从缓存中取（缓存是最新的日志）
	if offset < cacheCount {
		cacheEnd := offset + pageSize
		if cacheEnd > cacheCount {
			cacheEnd = cacheCount
		}
		result = append(result, cachedLogs[offset:cacheEnd]...)
	}

	// 如果启用了日志保存，缓存不够时从数据库补充
	if enabled {
		remaining := pageSize - len(result)
		if remaining > 0 {
			dbOffset := 0
			if offset > cacheCount {
				dbOffset = offset - cacheCount
			}

			query := db.GetDB().WithContext(ctx)
			if hasTimeFilter {
				query = query.Where("time >= ? AND time <= ?", *startTime, *endTime)
			}

			var dbLogs []model.RelayLog
			if err := query.Order("id DESC").Offset(dbOffset).Limit(remaining).Find(&dbLogs).Error; err != nil {
				return nil, err
			}
			result = append(result, dbLogs...)
		}
	}

	return result, nil
}

func sortRelayLogsNewestFirst(logs []model.RelayLog) {
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].Time == logs[j].Time {
			return logs[i].ID > logs[j].ID
		}
		return logs[i].Time > logs[j].Time
	})
}

func RelayLogClear(ctx context.Context) error {
	return relayLogService.Clear(ctx)
}

func (s *RelayLogService) Clear(ctx context.Context) error {
	s.cacheMu.Lock()
	s.cache = make([]model.RelayLog, 0, relayLogMaxSize)
	s.cacheMu.Unlock()
	return db.GetDB().WithContext(ctx).Where("1 = 1").Delete(&model.RelayLog{}).Error
}
