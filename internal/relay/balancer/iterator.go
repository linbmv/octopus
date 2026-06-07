package balancer

import (
	"fmt"
	"time"

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
	attempts []model.ChannelAttempt
	count    int
}

// NewIterator 创建负载均衡迭代器
// 自动处理：策略排序 + 粘性通道提前
func NewIterator(group model.Group, apiKeyID int, requestModel string) *Iterator {
	b := GetBalancer(group.Mode)
	candidates := b.Candidates(group.Items)

	stickyIdx := -1
	stickyKeyID := 0
	if group.SessionKeepTime > 0 {
		stickyTTL := time.Duration(group.SessionKeepTime) * time.Second
		if sticky := GetSticky(apiKeyID, requestModel, stickyTTL); sticky != nil {
			for i, item := range candidates {
				// 同渠道可能服务多个实际模型；仅当 actual model 也一致才复用 sticky，
				// 否则前缀不同会导致上游 prompt cache miss，失去粘性的意义。
				if item.ChannelID != sticky.ChannelID {
					continue
				}
				if sticky.ModelName != "" && sticky.ModelName != item.ModelName {
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
	it.attempts = append(it.attempts, model.ChannelAttempt{
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
	it.attempts = append(it.attempts, model.ChannelAttempt{
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
	if s.ended {
		return
	}
	s.ended = true
	s.attempt.Status = status
	s.attempt.Duration = int(time.Since(s.startTime).Milliseconds())
	s.attempt.Msg = msg
	// 记录首 token 用时（仅流式成功时有值）
	if s.firstTokenTime != nil {
		s.attempt.FirstTokenTime = int(s.firstTokenTime.Sub(s.startTime).Milliseconds())
	}
	s.iter.attempts = append(s.iter.attempts, s.attempt)
}

// Duration 返回从开始到现在的耗时
func (s *AttemptSpan) Duration() time.Duration {
	return time.Since(s.startTime)
}
