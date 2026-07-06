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
	compactStrategyOfficial     compactStrategy = compactStrategy(dbmodel.CompactStrategyOfficial)
	compactStrategyIncompatible compactStrategy = compactStrategy(dbmodel.CompactStrategyIncompatible)
)

type compactStrategyCacheKey struct {
	GroupItemID int
	ChannelID   int
	ChannelType llm.APIFormat
	BaseURL     string
	KeyID       int
	ModelName   string
}

const compactStrategyCacheTTL = time.Hour

type compactStrategyCacheEntry struct {
	strategy compactStrategy
	storedAt time.Time
}

var compactStrategyCache sync.Map // map[compactStrategyCacheKey]compactStrategyCacheEntry

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

func (ra *relayAttempt) compactStrategyCacheKey() compactStrategyCacheKey {
	cacheKey := compactStrategyKeyForItem(ra.channel, ra.groupItem, ra.usedKey)
	if ra.baseURL != "" {
		cacheKey.BaseURL = ra.baseURL
	}
	return cacheKey
}

// cachedCompactStrategy resolves the compact strategy for the current attempt
// from a two-layer cache:
//  1. an in-memory sync.Map keyed by (group item + channel + key + model),
//     populated by live requests via rememberCompactStrategy and bounded by
//     compactStrategyCacheTTL so a stale entry cannot permanently shadow a
//     newer probe result;
//  2. the DB-persisted groupItem.CompactStrategy, refreshed by the 24h
//     background probe, used when the in-memory entry is
//     missing or expired.
//
// An empty strategy (never probed / unknown) reports hasCached=false so the
// caller can evaluate the supported strategy list from scratch.
func (ra *relayAttempt) cachedCompactStrategy() (compactStrategy, bool) {
	cacheKey := ra.compactStrategyCacheKey()
	value, ok := compactStrategyCache.Load(cacheKey)
	if ok {
		entry, ok := value.(compactStrategyCacheEntry)
		if ok {
			if time.Since(entry.storedAt) <= compactStrategyCacheTTL {
				return entry.strategy, entry.strategy != ""
			}
		}
		compactStrategyCache.Delete(cacheKey)
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
	compactStrategyCache.Store(ra.compactStrategyCacheKey(), compactStrategyCacheEntry{
		strategy: strategy,
		storedAt: time.Now(),
	})

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
	// Canonical order lives in model.CompactStrategyOrder; relay only applies
	// request-time truncation when it has a usable cached official strategy.
	base := compactStrategySlice(dbmodel.CompactStrategyOrder(channelType))
	if len(base) == 0 {
		return nil
	}

	if hasCached && cached == compactStrategyOfficial {
		for idx, strategy := range base {
			if strategy == cached {
				return base[idx:]
			}
		}
	}

	return base
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

func resetCompactStrategyCacheForTest() {
	compactStrategyCache.Range(func(key, _ any) bool {
		compactStrategyCache.Delete(key)
		return true
	})
}
