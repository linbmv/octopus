package relay

import (
	"context"
	"sync"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
)

type compactStrategy string

const (
	compactStrategyOfficial        compactStrategy = compactStrategy(dbmodel.CompactStrategyOfficial)
	compactStrategyResponsesManual compactStrategy = compactStrategy(dbmodel.CompactStrategyResponsesManual)
	compactStrategyChatManual      compactStrategy = compactStrategy(dbmodel.CompactStrategyChatManual)
)

type compactStrategyCacheKey struct {
	GroupItemID int
	ChannelID   int
	ChannelType llm.APIFormat
	BaseURL     string
	KeyID       int
	ModelName   string
}

var compactStrategyCache sync.Map // map[compactStrategyCacheKey]compactStrategy

func compactStrategyKey(channel *dbmodel.Channel, key dbmodel.ChannelKey) compactStrategyCacheKey {
	cacheKey := compactStrategyCacheKey{KeyID: key.ID}
	if channel == nil {
		return cacheKey
	}
	cacheKey.ChannelID = channel.ID
	cacheKey.ChannelType = channel.Type
	cacheKey.BaseURL = channel.GetBaseUrl()
	return cacheKey
}

func compactStrategyKeyForItem(channel *dbmodel.Channel, item dbmodel.GroupItem, key dbmodel.ChannelKey) compactStrategyCacheKey {
	cacheKey := compactStrategyKey(channel, key)
	cacheKey.GroupItemID = item.ID
	cacheKey.ModelName = item.ModelName
	return cacheKey
}

func (ra *relayAttempt) cachedCompactStrategy() (compactStrategy, bool) {
	value, ok := compactStrategyCache.Load(compactStrategyKeyForItem(ra.channel, ra.groupItem, ra.usedKey))
	if ok {
		strategy, ok := value.(compactStrategy)
		return strategy, ok
	}
	if ra.groupItem.CompactStrategy == "" {
		return "", false
	}
	return compactStrategy(ra.groupItem.CompactStrategy), true
}

func (ra *relayAttempt) rememberCompactStrategy(ctx context.Context, strategy compactStrategy) {
	if strategy == "" {
		return
	}
	compactStrategyCache.Store(compactStrategyKeyForItem(ra.channel, ra.groupItem, ra.usedKey), strategy)

	persistedStrategy := dbmodel.CompactStrategy(strategy)
	if ra.groupItem.ID == 0 || ra.groupItem.GroupID == 0 || ra.groupItem.CompactStrategy == persistedStrategy {
		return
	}
	if err := op.GroupItemCompactStrategyUpdate(ra.groupItem.ID, ra.groupItem.GroupID, persistedStrategy, "", time.Now(), ctx); err != nil {
		log.Warnf("failed to persist compact strategy for group item %d: %v", ra.groupItem.ID, err)
		return
	}
	ra.groupItem.CompactStrategy = persistedStrategy
}

func compactStrategyOrder(channelType llm.APIFormat, cached compactStrategy, hasCached bool) []compactStrategy {
	if channelType == llm.APIFormatOpenAIChatCompletion {
		return []compactStrategy{compactStrategyChatManual}
	}

	switch channelType {
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
	default:
		return nil
	}

	if hasCached {
		switch cached {
		case compactStrategyOfficial:
			return []compactStrategy{
				compactStrategyOfficial,
				compactStrategyResponsesManual,
				compactStrategyChatManual,
			}
		case compactStrategyResponsesManual:
			return []compactStrategy{
				compactStrategyResponsesManual,
				compactStrategyChatManual,
			}
		case compactStrategyChatManual:
			return []compactStrategy{compactStrategyChatManual}
		}
	}

	return []compactStrategy{
		compactStrategyOfficial,
		compactStrategyResponsesManual,
		compactStrategyChatManual,
	}
}

func resetCompactStrategyCacheForTest() {
	compactStrategyCache.Range(func(key, _ any) bool {
		compactStrategyCache.Delete(key)
		return true
	})
}
