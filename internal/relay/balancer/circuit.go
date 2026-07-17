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
	// HalfOpenProbes 记录当前在途试探数。试探者可能在到达上游前被跳过或被
	// 客户端取消（不会走到 RecordSuccess/RecordFailure），必须通过
	// RecordProbeAbort 或租约超时归还名额，否则条目会永久卡在 HalfOpen。
	HalfOpenProbes     int
	HalfOpenLastProbe  time.Time
	mu                 sync.Mutex
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

// getHalfOpenMaxProbes 半开态允许的并发试探数上限（默认 2）。
// >1 可避免恢复期"唯一可用模型"被单试探串行化，导致其余请求全部失败。
func getHalfOpenMaxProbes() int {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerHalfOpenProbes)
	if err != nil || v <= 0 {
		return 2
	}
	return v
}

// getProbeLease 半开试探租约时长（默认 60s）。
// 在途试探超过该时长仍无终局结论时，视为试探者已丢失（如取消后未结算的
// 历史路径或未来新增的早退路径），允许放行新的试探，防止半开态永久冻结。
func getProbeLease() time.Duration {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerProbeLease)
	if err != nil || v <= 0 {
		return 60 * time.Second
	}
	return time.Duration(v) * time.Second
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
			entry.HalfOpenProbes = 1
			entry.HalfOpenLastProbe = now
			log.Infof("circuit breaker [%s] Open -> HalfOpen (cooldown %v elapsed)", key, cooldown)
			tripped = false
		} else {
			// 仍在冷却中
			tripped = true
			remaining = cooldown - elapsed
		}

	case StateHalfOpen:
		// 允许有限并发试探；在途试探超过租约仍无结论时视为丢失，放行新试探。
		// 这是防止"试探者被取消/跳过后未结算 → 半开态永久拒绝一切请求"的兜底。
		switch {
		case entry.HalfOpenProbes < getHalfOpenMaxProbes():
			entry.HalfOpenProbes++
			entry.HalfOpenLastProbe = now
			tripped = false
		case now.Sub(entry.HalfOpenLastProbe) >= getProbeLease():
			entry.HalfOpenLastProbe = now
			log.Warnf("circuit breaker [%s] probe lease expired, granting replacement probe", key)
			tripped = false
		default:
			tripped = true
		}

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
	entry.HalfOpenProbes = 0
	entry.HalfOpenLastProbe = time.Time{}
	entry.LastTouched = circuitNow()
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
}

// RecordProbeAbort 归还一个半开试探名额但不给出成败结论。
// 用于试探请求未到达上游即终止的路径：出站适配器构造失败、渠道限流预留失败、
// 客户端取消、client 级错误、自适应首字超时等。这些情况不能证明通道恢复或
// 仍然故障，但必须释放名额，否则半开态会在租约期内拒绝后续试探。
func RecordProbeAbort(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	entry := globalBreaker.get(key)
	if entry == nil {
		return
	}

	entry.mu.Lock()
	if entry.State == StateHalfOpen && entry.HalfOpenProbes > 0 {
		entry.HalfOpenProbes--
		log.Infof("circuit breaker [%s] probe aborted without verdict (in-flight probes=%d)", key, entry.HalfOpenProbes)
	}
	entry.LastTouched = circuitNow()
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
}

// ResetCircuit 立即清除指定 (channel, model) 的全部熔断状态（跨所有 key）。
// modelName 为空时清除该渠道全部模型。用于手动启用分组/渠道成员后要求
// 立即投入使用的场景：清除后下一个请求按 Closed 正常放行，不再等待冷却。
func ResetCircuit(channelID int, modelName string) {
	globalBreaker.resetCircuit(channelID, modelName)
}

func (s *breakerState) resetCircuit(channelID int, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for element := s.order.Front(); element != nil; {
		next := element.Next()
		key := element.Value.(*breakerRecord).key
		if key.ChannelID == channelID && (modelName == "" || key.ModelName == modelName) {
			s.removeElementLocked(element)
		}
		element = next
	}
}

// CircuitStateCounts 按状态统计当前熔断器条目数，供 /metrics 快照。
func CircuitStateCounts() (closed, open, halfOpen int) {
	return globalBreaker.stateCounts(circuitNow())
}

func (s *breakerState) stateCounts(now time.Time) (closed, open, halfOpen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for element := s.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*breakerRecord).entry
		entry.mu.Lock()
		state := entry.State
		expired := stateExpired(entry.LastTouched, now, circuitStateTTL)
		entry.mu.Unlock()
		if expired {
			continue
		}
		switch state {
		case StateOpen:
			open++
		case StateHalfOpen:
			halfOpen++
		default:
			closed++
		}
	}
	return closed, open, halfOpen
}

// CircuitSnapshotForChannel 返回指定渠道当前所有非 Closed 的熔断条目
//（含冻结剩余冷却），供渠道详情 UI 展示"哪些模型被冻结、还剩多久"。
func CircuitSnapshotForChannel(channelID int) []model.ChannelCircuitStatus {
	return globalBreaker.snapshotForChannel(channelID, circuitNow())
}

func (s *breakerState) snapshotForChannel(channelID int, now time.Time) []model.ChannelCircuitStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]model.ChannelCircuitStatus, 0)
	for element := s.order.Front(); element != nil; element = element.Next() {
		record := element.Value.(*breakerRecord)
		if record.key.ChannelID != channelID {
			continue
		}
		entry := record.entry
		entry.mu.Lock()
		state := entry.State
		expired := stateExpired(entry.LastTouched, now, circuitStateTTL)
		info := model.ChannelCircuitStatus{
			ChannelID:           record.key.ChannelID,
			ChannelKeyID:        record.key.KeyID,
			ModelName:           record.key.ModelName,
			ConsecutiveFailures: entry.ConsecutiveFailures,
			TripCount:           entry.TripCount,
		}
		if state == StateOpen {
			if remaining := GetCooldown(entry.TripCount) - now.Sub(entry.LastFailureTime); remaining > 0 {
				info.RemainingCooldownSeconds = int(remaining.Seconds())
			}
		}
		entry.mu.Unlock()
		if expired || state == StateClosed {
			continue
		}
		switch state {
		case StateOpen:
			info.State = "open"
		case StateHalfOpen:
			info.State = "half_open"
		}
		result = append(result, info)
	}
	return result
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
		entry.HalfOpenProbes = 0
		log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe failed, tripCount=%d, cooldown=%v)",
			key, entry.TripCount, GetCooldown(entry.TripCount))

	case StateOpen:
		// 理论上不应该在 Open 状态下接收到失败记录（请求应被拒绝），
		// 但为安全起见仍更新失败时间。
	}
	entry.mu.Unlock()
	globalBreaker.touch(key, entry)
}
