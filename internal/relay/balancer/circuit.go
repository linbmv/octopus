package balancer

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

const (
	circuitStateLimit = 16384
	circuitStateTTL   = 24 * time.Hour
)

var circuitNow = time.Now

// CircuitState 熔断器状态
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常通行
	StateOpen                         // 熔断中，拒绝所有请求
	StateHalfOpen                     // 半开，仅允许单个试探请求
)

// circuitEntry 单个熔断器条目
type circuitEntry struct {
	State               CircuitState
	ConsecutiveFailures int64
	LastFailureTime     time.Time
	LastTouched         time.Time
	TripCount           int // 累计熔断触发次数（用于指数退避）
	mu                  sync.Mutex
}

type breakerKey struct {
	ChannelID int
	KeyID     int
	ModelName string
}

func (k breakerKey) String() string {
	return fmt.Sprintf("%d:%d:%s", k.ChannelID, k.KeyID, k.ModelName)
}

type breakerRecord struct {
	key   breakerKey
	entry *circuitEntry
}

type breakerState struct {
	mu         sync.Mutex
	entries    map[breakerKey]*list.Element
	order      *list.List // least recently touched first
	operations uint64
}

func newBreakerState() *breakerState {
	return &breakerState{
		entries: make(map[breakerKey]*list.Element),
		order:   list.New(),
	}
}

// 全局熔断器存储。Typed key also lets configuration invalidation remove a
// channel without parsing delimiter-based strings.
var globalBreaker = newBreakerState()

// circuitKey 生成熔断器键：channelID:channelKeyID:modelName
func circuitKey(channelID, keyID int, modelName string) breakerKey {
	return breakerKey{ChannelID: channelID, KeyID: keyID, ModelName: modelName}
}

// getOrCreateEntry 获取或创建熔断器条目。
func getOrCreateEntry(key breakerKey, now time.Time) *circuitEntry {
	return globalBreaker.getOrCreate(key, now)
}

func (s *breakerState) getOrCreate(key breakerKey, now time.Time) *circuitEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.operations++
	if s.operations%runtimeStateSweepEvery == 0 {
		s.sweepExpiredLocked(now)
	}
	if element, ok := s.entries[key]; ok {
		s.order.MoveToBack(element)
		return element.Value.(*breakerRecord).entry
	}

	if len(s.entries) >= circuitStateLimit {
		s.sweepExpiredLocked(now)
	}
	if len(s.entries) >= circuitStateLimit {
		s.removeElementLocked(s.order.Front())
	}

	entry := &circuitEntry{State: StateClosed, LastTouched: now}
	element := s.order.PushBack(&breakerRecord{key: key, entry: entry})
	s.entries[key] = element
	return entry
}

func (s *breakerState) get(key breakerKey) *circuitEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		return nil
	}
	s.order.MoveToBack(element)
	return element.Value.(*breakerRecord).entry
}

func (s *breakerState) touch(key breakerKey, entry *circuitEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok || element.Value.(*breakerRecord).entry != entry {
		return
	}
	s.order.MoveToBack(element)
}

func (s *breakerState) removeIfSame(key breakerKey, entry *circuitEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok || element.Value.(*breakerRecord).entry != entry {
		return
	}
	s.removeElementLocked(element)
}

func (s *breakerState) sweepExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepExpiredLocked(now)
}

func (s *breakerState) sweepExpiredLocked(now time.Time) {
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(*breakerRecord).entry
		entry.mu.Lock()
		expired := stateExpired(entry.LastTouched, now, circuitStateTTL)
		entry.mu.Unlock()
		if expired {
			s.removeElementLocked(element)
		}
		element = next
	}
}

func (s *breakerState) invalidateChannel(channelID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		if element.Value.(*breakerRecord).key.ChannelID == channelID {
			s.removeElementLocked(element)
		}
		element = next
	}
}

func (s *breakerState) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	record := element.Value.(*breakerRecord)
	delete(s.entries, record.key)
	s.order.Remove(element)
}

func (s *breakerState) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[breakerKey]*list.Element)
	s.order.Init()
	s.operations = 0
}

func (s *breakerState) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// getThreshold 获取熔断阈值配置
func getThreshold() int64 {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerThreshold)
	if err != nil || v <= 0 {
		return 2
	}
	return int64(v)
}

// GetCooldown 获取当前冷却时间（带指数退避）
func GetCooldown(tripCount int) time.Duration {
	base, err := op.SettingGetInt(model.SettingKeyCircuitBreakerCooldown)
	if err != nil || base <= 0 {
		base = 60
	}
	maxCooldown, err := op.SettingGetInt(model.SettingKeyCircuitBreakerMaxCooldown)
	if err != nil || maxCooldown <= 0 {
		maxCooldown = 600
	}

	// 指数退避：baseCooldown * 2^(tripCount-1)
	cooldown := base
	if tripCount > 1 {
		shift := tripCount - 1
		if shift > 20 { // 防止溢出
			shift = 20
		}
		cooldown = base << shift
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return time.Duration(cooldown) * time.Second
}

// IsTripped 检查通道是否处于熔断状态
// 返回 tripped=true 表示该通道应被跳过，remaining 为剩余冷却时间
func IsTripped(channelID, keyID int, modelName string) (tripped bool, remaining time.Duration) {
	key := circuitKey(channelID, keyID, modelName)
	entry := globalBreaker.get(key)
	if entry == nil {
		return false, 0 // 无记录，视为 Closed
	}

	entry.mu.Lock()
	now := circuitNow()
	if stateExpired(entry.LastTouched, now, circuitStateTTL) {
		entry.mu.Unlock()
		globalBreaker.removeIfSame(key, entry)
		return false, 0
	}
	entry.LastTouched = now

	switch entry.State {
	case StateClosed:
		tripped = false

	case StateOpen:
		cooldown := GetCooldown(entry.TripCount)
		elapsed := now.Sub(entry.LastFailureTime)
		if elapsed >= cooldown {
			entry.State = StateHalfOpen
			log.Infof("circuit breaker [%s] Open -> HalfOpen (cooldown %v elapsed)", key, cooldown)
			tripped = false
		} else {
			// 仍在冷却中
			tripped = true
			remaining = cooldown - elapsed
		}

	case StateHalfOpen:
		// 已有试探请求在进行中，拒绝其他请求
		tripped = true

	default:
		tripped = false
	}
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
	return tripped, remaining
}

// RecordSuccess 记录成功，重置熔断器状态
func RecordSuccess(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	entry := globalBreaker.get(key)
	if entry == nil {
		return
	}

	entry.mu.Lock()
	if entry.State == StateHalfOpen {
		log.Infof("circuit breaker [%s] HalfOpen -> Closed (probe succeeded)", key)
	}

	// 重置全部状态
	entry.State = StateClosed
	entry.ConsecutiveFailures = 0
	entry.TripCount = 0
	entry.LastTouched = circuitNow()
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
}

// RecordFailure 记录失败，可能触发熔断
func RecordFailure(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	entry := getOrCreateEntry(key, circuitNow())

	entry.mu.Lock()
	now := circuitNow()
	entry.LastFailureTime = now
	entry.LastTouched = now

	switch entry.State {
	case StateClosed:
		entry.ConsecutiveFailures++
		threshold := getThreshold()
		if entry.ConsecutiveFailures >= threshold {
			entry.State = StateOpen
			entry.TripCount++
			log.Warnf("circuit breaker [%s] Closed -> Open (failures=%d >= threshold=%d, tripCount=%d, cooldown=%v)",
				key, entry.ConsecutiveFailures, threshold, entry.TripCount, GetCooldown(entry.TripCount))
		}

	case StateHalfOpen:
		// 试探失败，重新进入 Open 状态，TripCount 递增（冷却时间翻倍）
		entry.State = StateOpen
		entry.TripCount++
		entry.ConsecutiveFailures = 0 // 重新开始计数
		log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe failed, tripCount=%d, cooldown=%v)",
			key, entry.TripCount, GetCooldown(entry.TripCount))

	case StateOpen:
		// 理论上不应该在 Open 状态下接收到失败记录（请求应被拒绝），
		// 但为安全起见仍更新失败时间。
	}
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
}
