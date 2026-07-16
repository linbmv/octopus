package op

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/snowflake"
	"gorm.io/gorm/clause"
)

const (
	relayLogMaxSize           = 20
	relayLogCacheHardLimit    = 100
	relayLogCacheRetainOnFull = relayLogCacheHardLimit / 2
	relayLogNotifyQueueSize   = 64
	relayLogSubscriberBuffer  = 10

	relayLogDropNotifyQueue = "notification_queue_full"
	relayLogDropSubscriber  = "slow_subscriber"
	relayLogDropCache       = "memory_cache_evicted"
)

var relayLogService = NewRelayLogService()

var ErrRelayLogStreamTokenCapacity = errors.New("too many unconsumed relay log stream tokens")

type RelayLogService struct {
	cache   []model.RelayLog
	cacheMu sync.Mutex

	flushMu     sync.Mutex
	flushSignal chan struct{}

	workerMu     sync.Mutex
	workerCancel context.CancelFunc
	workerDone   chan struct{}

	notifyQueue chan model.RelayLog

	subscribers   map[chan model.RelayLog]struct{}
	subscribersMu sync.RWMutex

	notifyQueueDropped atomic.Uint64
	subscriberDropped  atomic.Uint64
	cacheDropped       atomic.Uint64
	flushFailures      atomic.Uint64

	streamTokens   map[string]time.Time
	streamTokensMu sync.RWMutex
}

// streamTokenTTL 限定一次性流 token 从签发到建立 SSE 连接的窗口。
// 没有 TTL 时，取 token 后不连接（网络中断/前端重试）会让 token 永久驻留内存。
const streamTokenTTL = 2 * time.Minute

const relayLogStreamTokenMaxEntries = 256

func NewRelayLogService() *RelayLogService {
	return &RelayLogService{
		cache:        make([]model.RelayLog, 0, relayLogMaxSize),
		flushSignal:  make(chan struct{}, 1),
		notifyQueue:  make(chan model.RelayLog, relayLogNotifyQueueSize),
		subscribers:  make(map[chan model.RelayLog]struct{}),
		streamTokens: make(map[string]time.Time),
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
	now := time.Now()
	// Sweep expired tokens, then enforce a hard cap. TTL alone is not a bound:
	// an authenticated client could otherwise issue unbounded tokens inside the
	// two-minute window and grow this map until the process is exhausted.
	for stale, expireAt := range s.streamTokens {
		if now.After(expireAt) {
			delete(s.streamTokens, stale)
		}
	}
	if len(s.streamTokens) >= relayLogStreamTokenMaxEntries {
		s.streamTokensMu.Unlock()
		return "", ErrRelayLogStreamTokenCapacity
	}
	if _, collision := s.streamTokens[token]; collision {
		s.streamTokensMu.Unlock()
		return "", fmt.Errorf("relay log stream token collision")
	}
	s.streamTokens[token] = now.Add(streamTokenTTL)
	s.streamTokensMu.Unlock()

	return token, nil
}

func RelayLogStreamTokenConsume(token string) bool {
	return relayLogService.StreamTokenConsume(token)
}

func (s *RelayLogService) StreamTokenConsume(token string) bool {
	s.streamTokensMu.Lock()
	defer s.streamTokensMu.Unlock()
	expireAt, ok := s.streamTokens[token]
	if !ok {
		return false
	}
	// Consume under the same lock as verification so two simultaneous SSE
	// handshakes cannot both use the same one-time token.
	delete(s.streamTokens, token)
	return !time.Now().After(expireAt)
}

func RelayLogSubscribe() chan model.RelayLog {
	return relayLogService.Subscribe()
}

func (s *RelayLogService) Subscribe() chan model.RelayLog {
	ch := make(chan model.RelayLog, relayLogSubscriberBuffer)
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
			s.recordRelayLogDrop(relayLogDropSubscriber, 1)
		}
	}
}

func (s *RelayLogService) enqueueNotification(relayLog model.RelayLog) {
	select {
	case s.notifyQueue <- relayLog:
	default:
		s.recordRelayLogDrop(relayLogDropNotifyQueue, 1)
	}
}

func (s *RelayLogService) recordRelayLogDrop(reason string, count uint64) {
	if count == 0 {
		return
	}
	switch reason {
	case relayLogDropNotifyQueue:
		s.notifyQueueDropped.Add(count)
	case relayLogDropSubscriber:
		s.subscriberDropped.Add(count)
	case relayLogDropCache:
		s.cacheDropped.Add(count)
	}
	metrics.RecordRelayLogDropped(reason, count)
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
	s.cacheMu.Unlock()

	result := db.GetDB().WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&batch)
	if result.Error != nil {
		s.flushFailures.Add(1)
		metrics.RecordRelayLogFlushFailure()
		return result.Error
	}

	flushedIDs := make(map[int64]struct{}, len(batch))
	for i := range batch {
		flushedIDs[batch[i].ID] = struct{}{}
	}
	s.cacheMu.Lock()
	remaining := make([]model.RelayLog, 0, len(s.cache))
	for i := range s.cache {
		if _, flushed := flushedIDs[s.cache[i].ID]; !flushed {
			remaining = append(remaining, s.cache[i])
		}
	}
	s.cache = remaining
	if len(remaining) == 0 {
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

func (s *RelayLogService) StartFlushWorker() {
	_ = s.StartFlushWorkerContext(context.Background())
}

func (s *RelayLogService) StartFlushWorkerContext(parent context.Context) error {
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
			return errors.New("relay log workers are still running")
		}
	}

	ctx, cancel := context.WithCancel(parent)
	doneCh := make(chan struct{})
	s.workerCancel = cancel
	s.workerDone = doneCh
	go s.runWorkers(ctx, doneCh)
	return nil
}

func (s *RelayLogService) StopFlushWorker(ctx context.Context) error {
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

func (s *RelayLogService) runWorkers(ctx context.Context, doneCh chan<- struct{}) {
	notifyDone := make(chan struct{})
	go func() {
		s.notificationWorker(ctx)
		close(notifyDone)
	}()
	s.flushWorker(ctx)
	<-notifyDone
	close(doneCh)
}

func (s *RelayLogService) flushWorker(ctx context.Context) {
	for {
		select {
		case <-s.flushSignal:
			flushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.flushToDB(flushCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Errorf("relay log async flush error: %v", err)
			}
			cancel()
		case <-ctx.Done():
			return
		}
	}
}

func (s *RelayLogService) notificationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.drainNotificationQueue()
			return
		default:
		}

		select {
		case relayLog := <-s.notifyQueue:
			s.notifySubscribers(relayLog)
		case <-ctx.Done():
			s.drainNotificationQueue()
			return
		}
	}
}

func (s *RelayLogService) drainNotificationQueue() {
	// The queue is bounded, so shutdown work is bounded as well. Producers are
	// stopped before this worker in the application shutdown order.
	pending := len(s.notifyQueue)
	for i := 0; i < pending; i++ {
		select {
		case relayLog := <-s.notifyQueue:
			s.notifySubscribers(relayLog)
		default:
			return
		}
	}
}

func RelayLogAdd(ctx context.Context, relayLog model.RelayLog) error {
	return relayLogService.Add(ctx, relayLog)
}

func (s *RelayLogService) Add(ctx context.Context, relayLog model.RelayLog) error {
	contentMode, err := RelayLogContentModeGet()
	if err != nil {
		return err
	}
	if contentMode == model.RelayLogContentModeDisabled {
		return nil
	}
	relayLog = applyRelayLogContentPolicy(relayLog, contentMode)

	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return err
	}
	relayLog.ID = snowflake.GenerateID()
	s.enqueueNotification(relayLog)

	s.cacheMu.Lock()
	s.cache = append(s.cache, relayLog)
	shouldFlush := enabled && len(s.cache) >= relayLogMaxSize
	dropped := 0
	if len(s.cache) >= relayLogCacheHardLimit {
		// Hard-limit policy: evict the oldest half and retain the newest half.
		// This bounds memory while avoiding an allocation/copy on every new log
		// during a prolonged database outage.
		dropped = s.trimCacheLocked(relayLogCacheRetainOnFull, relayLogCacheHardLimit)
	}
	s.cacheMu.Unlock()
	if dropped > 0 {
		s.recordRelayLogDrop(relayLogDropCache, uint64(dropped))
	}
	if shouldFlush {
		s.signalFlush()
	}
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

	// 如果未启用日志保存，仍保留防御性硬上限。Add 正常会先执行相同策略。
	s.cacheMu.Lock()
	dropped := 0
	if len(s.cache) >= relayLogCacheHardLimit {
		dropped = s.trimCacheLocked(relayLogCacheRetainOnFull, relayLogCacheHardLimit)
	}
	s.cacheMu.Unlock()
	if dropped > 0 {
		s.recordRelayLogDrop(relayLogDropCache, uint64(dropped))
	}

	return nil
}

// trimCacheLocked drops the oldest entries and rebuilds the backing array so
// large request/response strings are no longer retained. s.cacheMu must be held.
func (s *RelayLogService) trimCacheLocked(keepSize, capacity int) int {
	if keepSize < 0 {
		keepSize = 0
	}
	if keepSize >= len(s.cache) {
		return 0
	}
	dropped := len(s.cache) - keepSize
	newCache := make([]model.RelayLog, keepSize, capacity)
	copy(newCache, s.cache[len(s.cache)-keepSize:])
	s.cache = newCache
	return dropped
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

type RelayLogCursorPage struct {
	Items      []model.RelayLog
	NextCursor int64
	HasMore    bool
}

// RelayLogListCursor performs stable keyset pagination. beforeID=0 starts at
// the newest log; subsequent pages contain IDs strictly below the returned
// cursor, so concurrent inserts cannot shift an offset and create gaps.
func RelayLogListCursor(ctx context.Context, startTime, endTime *int, beforeID int64, pageSize int) (RelayLogCursorPage, error) {
	return relayLogService.ListCursor(ctx, startTime, endTime, beforeID, pageSize)
}

func (s *RelayLogService) ListCursor(ctx context.Context, startTime, endTime *int, beforeID int64, pageSize int) (RelayLogCursorPage, error) {
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return RelayLogCursorPage{}, err
	}
	hasTimeFilter := startTime != nil && endTime != nil

	s.cacheMu.Lock()
	cachedLogs := make([]model.RelayLog, 0, len(s.cache))
	for _, entry := range s.cache {
		if beforeID > 0 && entry.ID >= beforeID {
			continue
		}
		if hasTimeFilter && (entry.Time < int64(*startTime) || entry.Time > int64(*endTime)) {
			continue
		}
		cachedLogs = append(cachedLogs, entry)
	}
	s.cacheMu.Unlock()

	entries := make(map[int64]model.RelayLog, len(cachedLogs)+pageSize+1)
	for _, entry := range cachedLogs {
		entries[entry.ID] = entry
	}
	if enabled {
		query := db.GetDB().WithContext(ctx)
		if beforeID > 0 {
			query = query.Where("id < ?", beforeID)
		}
		if hasTimeFilter {
			query = query.Where("time >= ? AND time <= ?", *startTime, *endTime)
		}
		// Cache and DB can overlap while a flush completes. Fetch enough rows to
		// compensate for every cached duplicate plus one has-more sentinel.
		var dbLogs []model.RelayLog
		if err := query.Order("id DESC").Limit(pageSize + len(cachedLogs) + 1).Find(&dbLogs).Error; err != nil {
			return RelayLogCursorPage{}, err
		}
		for _, entry := range dbLogs {
			entries[entry.ID] = entry
		}
	}

	merged := make([]model.RelayLog, 0, len(entries))
	for _, entry := range entries {
		merged = append(merged, entry)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })
	hasMore := len(merged) > pageSize
	if hasMore {
		merged = merged[:pageSize]
	}
	nextCursor := int64(0)
	if hasMore && len(merged) > 0 {
		nextCursor = merged[len(merged)-1].ID
	}
	return RelayLogCursorPage{Items: merged, NextCursor: nextCursor, HasMore: hasMore}, nil
}

// RelayLogListAfter returns missed logs in ascending ID order for SSE
// reconnect. If more than limit entries were missed, Truncated is true and the
// newest limit entries are returned; the browser then refreshes cursor pages.
func RelayLogListAfter(ctx context.Context, afterID int64, limit int) (items []model.RelayLog, truncated bool, err error) {
	return relayLogService.ListAfter(ctx, afterID, limit)
}

func (s *RelayLogService) ListAfter(ctx context.Context, afterID int64, limit int) ([]model.RelayLog, bool, error) {
	if afterID <= 0 || limit <= 0 {
		return nil, false, nil
	}
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return nil, false, err
	}

	s.cacheMu.Lock()
	cachedLogs := make([]model.RelayLog, 0, len(s.cache))
	for _, entry := range s.cache {
		if entry.ID > afterID {
			cachedLogs = append(cachedLogs, entry)
		}
	}
	s.cacheMu.Unlock()

	entries := make(map[int64]model.RelayLog, len(cachedLogs)+limit+1)
	for _, entry := range cachedLogs {
		entries[entry.ID] = entry
	}
	if enabled {
		var dbLogs []model.RelayLog
		if err := db.GetDB().WithContext(ctx).
			Where("id > ?", afterID).
			Order("id DESC").
			Limit(limit + len(cachedLogs) + 1).
			Find(&dbLogs).Error; err != nil {
			return nil, false, err
		}
		for _, entry := range dbLogs {
			entries[entry.ID] = entry
		}
	}

	merged := make([]model.RelayLog, 0, len(entries))
	for _, entry := range entries {
		merged = append(merged, entry)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	truncated := len(merged) > limit
	if truncated {
		merged = merged[len(merged)-limit:]
	}
	return merged, truncated, nil
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
