package relay

import (
	"context"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
)

type compactStrategy string

type compactStrategyUpdateFunc func(itemID, groupID int, strategy dbmodel.CompactStrategy, updatedAt time.Time, ctx context.Context) error

// rememberCompactStrategy 把探明可用的 compact 策略持久化到 group item，
// 供 request.go 的 compactCandidateRanks 在候选排序时优先官方兼容渠道。
// 247c02b 后 compact 只走官方端点，原进程内策略缓存已无读方，随之移除。
func (ra *relayAttempt) rememberCompactStrategy(ctx context.Context, strategy compactStrategy) {
	if strategy == "" {
		return
	}
	persistedStrategy := dbmodel.CompactStrategy(strategy)
	if ra.groupItem.ID == 0 || ra.groupItem.GroupID == 0 || ra.groupItem.CompactStrategy == persistedStrategy {
		return
	}
	update := compactStrategyUpdateFunc(op.GroupItemCompactStrategyUpdate)
	if ra.compactStrategyUpdater != nil {
		update = ra.compactStrategyUpdater
	}
	if err := update(ra.groupItem.ID, ra.groupItem.GroupID, persistedStrategy, time.Now(), ctx); err != nil {
		log.Warnf("failed to persist compact strategy for group item %d: %v", ra.groupItem.ID, err)
		return
	}
	ra.groupItem.CompactStrategy = persistedStrategy
}

func (ra *relayAttempt) applyCompactCompatibilityDecision(ctx context.Context, decision ErrorDecision) {
	if decision.CompactAction != CompactCompatibilityMarkIncompatible {
		return
	}
	ra.rememberCompactStrategy(ctx, compactStrategy(dbmodel.CompactStrategyIncompatible))
}

// compactStrategyOrder 返回渠道类型支持的 compact 策略序，
// canonical 顺序的唯一来源是 model.CompactStrategyOrder。
func compactStrategyOrder(channelType llm.APIFormat) []compactStrategy {
	return compactStrategySlice(dbmodel.CompactStrategyOrder(channelType))
}

func compactStrategySlice(strategies []dbmodel.CompactStrategy) []compactStrategy {
	if len(strategies) == 0 {
		return nil
	}
	out := make([]compactStrategy, 0, len(strategies))
	for _, strategy := range strategies {
		out = append(out, compactStrategy(strategy))
	}
	return out
}
