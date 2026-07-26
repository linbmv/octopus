package balancer

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bestruirui/octopus/internal/model"
)

// Iterator 统一的负载均衡迭代器
// 内部编排：策略排序 + 粘性优先 + 决策追踪
type Iterator struct {
	candidates  []model.GroupItem
	index       int
	stickyIdx   int    // 粘性通道在 candidates 中的位置，-1 表示无
	stickyKeyID int    // 粘性通道对应的 key ID，0 表示无
	modelName   string // 请求模型名（用于熔断检查）

	// 内嵌追踪
	attempts  []model.ChannelAttempt
	count     int
	eventSink func(model.ChannelAttempt)
}

// SetAttemptEventSink mirrors every future iterator decision to a request-level
// timeline. The iterator keeps its local history for backwards compatibility;
// the sink is intentionally write-only so callers cannot mutate it.
func (it *Iterator) SetAttemptEventSink(sink func(model.ChannelAttempt)) {
	if it != nil {
		it.eventSink = sink
	}
}

func (it *Iterator) recordAttempt(attempt model.ChannelAttempt) {
	it.attempts = append(it.attempts, attempt)
	if it.eventSink != nil {
		it.eventSink(attempt)
	}
}

// NewIterator 创建负载均衡迭代器
// 自动处理：策略排序 + 粘性通道提前
func NewIterator(group model.Group, apiKeyID int, requestModel string) *Iterator {
	return NewIteratorWithCandidateRanks(group, apiKeyID, requestModel, nil)
}

// NewIteratorWithCandidateRanks 创建带候选能力分层的迭代器。
// ranks 仅作为第一层排序键；同一 rank 内仍保留原分组策略产生的顺序。
func NewIteratorWithCandidateRanks(group model.Group, apiKeyID int, requestModel string, ranks map[int]int) *Iterator {
	b := GetBalancer(group.Mode)
	candidates := b.Candidates(group.Items)
	return NewIteratorFromCandidates(group, apiKeyID, requestModel, candidates, ranks)
}

func NewIteratorFromCandidates(group model.Group, apiKeyID int, requestModel string, candidates []model.GroupItem, ranks map[int]int) *Iterator {
	return NewIteratorFromCandidatesWithSession(group, apiKeyID, requestModel, "", candidates, ranks, nil)
}

// NewIteratorFromCandidatesWithSession adds request-scoped affinity and policy
// metadata while preserving the original constructor for callers that do not
// have a stable session identity.
func NewIteratorFromCandidatesWithSession(group model.Group, apiKeyID int, requestModel, sessionID string, candidates []model.GroupItem, ranks map[int]int, policyProfiles map[int]string) *Iterator {
	applyCandidateRanks(candidates, ranks)

	stickyIdx := -1
	stickyKeyID := 0
	if group.SessionKeepTime > 0 {
		stickyTTL := time.Duration(group.SessionKeepTime) * time.Second
		if sticky := GetStickyForSession(apiKeyID, requestModel, sessionID, stickyTTL); sticky != nil {
			for i, item := range candidates {
				// 同渠道可能服务多个实际模型；仅当 actual model 也一致才复用 sticky，
				// 否则前缀不同会导致上游 prompt cache miss，失去粘性的意义。
				if item.ChannelID != sticky.ChannelID {
					continue
				}
				if sticky.ModelName != "" && sticky.ModelName != item.ModelName {
					continue
				}
				if len(candidates) > 0 && rankOfCandidate(item, ranks) != rankOfCandidate(candidates[0], ranks) {
					continue
				}
				if len(candidates) > 0 && item.Priority != candidates[0].Priority {
					continue
				}
				if len(policyProfiles) > 0 && policyProfiles[item.ID] != policyProfiles[candidates[0].ID] {
					continue
				}
				if i > 0 {
					// 将粘性通道移到最前面
					stickyItem := candidates[i]
					copy(candidates[1:i+1], candidates[0:i])
					candidates[0] = stickyItem
				}
				stickyIdx = 0
				stickyKeyID = sticky.ChannelKeyID
				break
			}
		}
	}

	return &Iterator{
		candidates:  candidates,
		index:       -1,
		stickyIdx:   stickyIdx,
		stickyKeyID: stickyKeyID,
		modelName:   requestModel,
	}
}

// HardUnusableRank 及以上的 rank 表示"硬不可用"（如 compact 已确认不兼容），
// 排序时无视用户 Priority 直接排到最后。低于该值的 rank 属于软证据
// （capability 探测结果），仅在同 Priority 内起次级排序作用。
const HardUnusableRank = 1 << 20

func applyCandidateRanks(candidates []model.GroupItem, ranks map[int]int) {
	if len(candidates) <= 1 || len(ranks) == 0 {
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := rankOfCandidate(candidates[i], ranks), rankOfCandidate(candidates[j], ranks)
		// 硬不可用的候选（如 compact 不兼容）排最后，无视 Priority
		hardI, hardJ := ri >= HardUnusableRank, rj >= HardUnusableRank
		if hardI != hardJ {
			return !hardI
		}
		// Priority 为第一排序键（升序：值越小越优先）
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		// Priority 相同时，按 capability rank 排序
		return ri < rj
	})
}

func rankOfCandidate(item model.GroupItem, ranks map[int]int) int {
	if len(ranks) == 0 {
		return 0
	}
	if rank, ok := ranks[item.ID]; ok {
		return rank
	}
	return 1 << 30
}

// Next 移动到下一个候选，返回 false 表示遍历完成
func (it *Iterator) Next() bool {
	it.index++
	return it.index < len(it.candidates)
}

// Item 返回当前候选的 GroupItem
func (it *Iterator) Item() model.GroupItem {
	return it.candidates[it.index]
}

// IsSticky 当前候选是否为粘性通道
func (it *Iterator) IsSticky() bool {
	return it.stickyIdx >= 0 && it.index == it.stickyIdx
}

// StickyKeyID 返回当前粘性通道对应的 key ID
func (it *Iterator) StickyKeyID() int {
	if !it.IsSticky() {
		return 0
	}
	return it.stickyKeyID
}

// Len 返回候选列表长度
func (it *Iterator) Len() int {
	return len(it.candidates)
}

// Index 返回当前迭代位置（0-based）
func (it *Iterator) Index() int {
	return it.index
}

// Skip 记录当前通道被跳过（通道禁用、无Key、类型不兼容等）
func (it *Iterator) Skip(channelID, channelKeyID int, channelName, msg string) {
	it.count++
	it.recordAttempt(model.ChannelAttempt{
		ChannelID:    channelID,
		ChannelKeyID: channelKeyID,
		ChannelName:  channelName,
		ModelName:    it.candidates[it.index].ModelName,
		AttemptNum:   it.count,
		Status:       model.AttemptSkipped,
		Sticky:       it.IsSticky(),
		Msg:          msg,
	})
}

// SkipCircuitBreak 检查熔断状态，若已熔断自动记录（含剩余冷却时间）并返回 true
func (it *Iterator) SkipCircuitBreak(channelID, channelKeyID int, channelName, channelKeyRemark string) bool {
	modelName := it.candidates[it.index].ModelName
	tripped, remaining := IsTripped(channelID, channelKeyID, modelName)
	if !tripped {
		return false
	}
	msg := "circuit breaker tripped"
	if remaining > 0 {
		msg = fmt.Sprintf("circuit breaker tripped, remaining cooldown: %ds", int(remaining.Seconds()))
	}
	it.count++
	it.recordAttempt(model.ChannelAttempt{
		ChannelID:        channelID,
		ChannelKeyID:     channelKeyID,
		ChannelKeyRemark: channelKeyRemark,
		ChannelName:      channelName,
		ModelName:        modelName,
		AttemptNum:       it.count,
		Status:           model.AttemptCircuitBreak,
		Sticky:           it.IsSticky(),
		Msg:              msg,
	})
	return true
}

// StartAttempt 开始一次真实转发尝试，返回 Span 用于记录结果
func (it *Iterator) StartAttempt(channelID, channelKeyID int, channelName, channelKeyRemark string) *AttemptSpan {
	it.count++
	return &AttemptSpan{
		attempt: model.ChannelAttempt{
			ChannelID:        channelID,
			ChannelKeyID:     channelKeyID,
			ChannelKeyRemark: channelKeyRemark,
			ChannelName:      channelName,
			ModelName:        it.candidates[it.index].ModelName,
			AttemptNum:       it.count,
			Sticky:           it.IsSticky(),
		},
		startTime: time.Now(),
		iter:      it,
	}
}

// Attempts 返回所有决策记录（交给日志模块持久化）
func (it *Iterator) Attempts() []model.ChannelAttempt {
	return it.attempts
}

// AttemptSpan 管理单次通道尝试的生命周期（计时、状态、结果）
type AttemptSpan struct {
	attempt        model.ChannelAttempt
	startTime      time.Time
	iter           *Iterator
	ended          bool
	firstTokenTime *time.Time // 首token时间，用于记录到 attempt
}

// SetRoutingMetadata records the routing decision alongside the attempt result
// so operators can distinguish endpoint/key failover from channel health
// penalties without parsing free-form log messages.
func (s *AttemptSpan) SetRoutingMetadata(baseURL, action string, healthPenalty bool, selectionReason string) {
	if s == nil || s.ended {
		return
	}
	s.attempt.BaseURL = baseURL
	s.attempt.Action = action
	s.attempt.HealthPenalty = healthPenalty
	s.attempt.SelectionReason = selectionReason
}

// RecordFirstToken 记录首 token 时间（用于计算首token用时和判断健康粘性）
func (s *AttemptSpan) RecordFirstToken(t time.Time) {
	if s.firstTokenTime == nil {
		s.firstTokenTime = &t
	}
}

// FirstTokenDuration 返回当前 attempt 从开始到首 token 的耗时。
func (s *AttemptSpan) FirstTokenDuration() (time.Duration, bool) {
	if s.firstTokenTime == nil {
		return 0, false
	}
	return s.firstTokenTime.Sub(s.startTime), true
}

// End 结束尝试：设置状态，自动计算耗时，追加到 Iterator
func (s *AttemptSpan) End(status model.AttemptStatus, msg string) {
	s.end(status, msg, "", "")
}

// EndClassified ends a failed upstream attempt and persists the classifier's
// scope and reason alongside the human-readable transport error. Keeping the
// values on the attempt makes observability independent of unstructured Msg.
func (s *AttemptSpan) EndClassified(status model.AttemptStatus, msg string, level model.AttemptErrorLevel, reason string) {
	s.end(status, msg, level, boundedErrorReason(reason))
}

const maxAttemptErrorReasonRunes = 256

func boundedErrorReason(reason string) string {
	reason = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, reason))
	runes := []rune(reason)
	if len(runes) > maxAttemptErrorReasonRunes {
		return string(runes[:maxAttemptErrorReasonRunes])
	}
	return reason
}

func (s *AttemptSpan) end(status model.AttemptStatus, msg string, level model.AttemptErrorLevel, reason string) {
	if s.ended {
		return
	}
	s.ended = true
	s.attempt.Status = status
	s.attempt.Duration = int(time.Since(s.startTime).Milliseconds())
	s.attempt.Msg = msg
	s.attempt.ErrorLevel = level
	s.attempt.ErrorReason = reason
	// 记录首 token 用时（仅流式成功时有值）
	if s.firstTokenTime != nil {
		s.attempt.FirstTokenTime = int(s.firstTokenTime.Sub(s.startTime).Milliseconds())
	}
	s.iter.recordAttempt(s.attempt)
}

// Duration 返回从开始到现在的耗时
func (s *AttemptSpan) Duration() time.Duration {
	return time.Since(s.startTime)
}

// SkipFor 记录指定 item 的跳过（允许为非当前 item 记录 attempt）
// 用于虚拟渠道：虚拟渠道本身不是真实 attempt，但需要记录重定向决策
func (it *Iterator) SkipFor(
	item model.GroupItem,
	sticky bool,
	channelID int,
	channelKeyID int,
	channelName string,
	msg string,
) {
	it.count++
	it.recordAttempt(model.ChannelAttempt{
		ChannelID:    channelID,
		ChannelKeyID: channelKeyID,
		ChannelName:  channelName,
		ModelName:    item.ModelName,
		AttemptNum:   it.count,
		Status:       model.AttemptSkipped,
		Sticky:       sticky,
		Msg:          msg,
	})
}

// RedirectFor 记录虚拟渠道重定向到目标分组
func (it *Iterator) RedirectFor(
	item model.GroupItem,
	virtualChannelID int,
	virtualChannelName string,
	targetGroupID int,
	targetGroupName string,
	depth int,
	msg string,
) {
	it.count++
	it.recordAttempt(model.ChannelAttempt{
		ChannelID:   virtualChannelID,
		ChannelName: virtualChannelName,
		ModelName:   item.ModelName,
		AttemptNum:  it.count,
		Status:      model.AttemptRedirect,
		Msg:         fmt.Sprintf("redirect to group %d(%s), depth=%d: %s", targetGroupID, targetGroupName, depth, msg),
	})
}

// SkipCircuitBreakFor 检查熔断状态并记录（允许为指定 item 检查和记录）
func (it *Iterator) SkipCircuitBreakFor(
	item model.GroupItem,
	sticky bool,
	channelID int,
	channelKeyID int,
	channelName string,
	channelKeyRemark string,
) bool {
	tripped, remaining := IsTripped(channelID, channelKeyID, item.ModelName)
	if !tripped {
		return false
	}

	msg := "circuit breaker tripped"
	if remaining > 0 {
		msg = fmt.Sprintf("circuit breaker tripped, remaining cooldown: %ds", int(remaining.Seconds()))
	}

	it.count++
	it.recordAttempt(model.ChannelAttempt{
		ChannelID:        channelID,
		ChannelKeyID:     channelKeyID,
		ChannelKeyRemark: channelKeyRemark,
		ChannelName:      channelName,
		ModelName:        item.ModelName,
		AttemptNum:       it.count,
		Status:           model.AttemptCircuitBreak,
		Sticky:           sticky,
		Msg:              msg,
	})
	return true
}

// StartAttemptFor 开始一次真实转发尝试（允许为指定 item 开始 attempt）
func (it *Iterator) StartAttemptFor(
	item model.GroupItem,
	sticky bool,
	channelID int,
	channelKeyID int,
	channelName string,
	channelKeyRemark string,
) *AttemptSpan {
	it.count++
	return &AttemptSpan{
		attempt: model.ChannelAttempt{
			ChannelID:        channelID,
			ChannelKeyID:     channelKeyID,
			ChannelKeyRemark: channelKeyRemark,
			ChannelName:      channelName,
			ModelName:        item.ModelName,
			AttemptNum:       it.count,
			Sticky:           sticky,
		},
		startTime: time.Now(),
		iter:      it,
	}
}
