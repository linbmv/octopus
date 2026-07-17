package op

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestRankCacheVersionAndTTLSemantics(t *testing.T) {
	cache := newRankCache()
	now := time.Unix(1000000, 0)
	ranks := map[int]int{1: 0, 2: 2}

	cache.put("k", ranks, 7, now)

	if got, ok := cache.get("k", 7, now.Add(time.Second)); !ok || got[1] != 0 || got[2] != 2 {
		t.Fatalf("同版本 TTL 内应命中, got=%v ok=%v", got, ok)
	}
	if _, ok := cache.get("k", 8, now.Add(time.Second)); ok {
		t.Fatal("版本不匹配必须视为失效（evidence 已变更）")
	}
	// 版本失效应同时把过期条目从表中删除。
	cache.put("k", ranks, 7, now)
	if _, ok := cache.get("k", 7, now.Add(capabilityRankCacheTTL+time.Second)); ok {
		t.Fatal("TTL 过期必须失效（evidence expires_at 随时间推移）")
	}
}

func TestRankCacheOverflowClearsAll(t *testing.T) {
	cache := newRankCache()
	now := time.Unix(1000000, 0)
	for i := 0; i < capabilityRankCacheMaxEntries; i++ {
		cache.put(capabilityRankCacheKey(&model.Channel{ID: i}, []int{1}, "m", []model.Capability{model.CapabilityTool}, "https://e"), map[int]int{1: 1}, 1, now)
	}
	cache.put("overflow", map[int]int{1: 1}, 1, now)
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size != 1 {
		t.Fatalf("容量溢出应整体清空后仅存新条目, got %d", size)
	}
}

func TestRankCacheKeyCanonicalOrdering(t *testing.T) {
	channel := &model.Channel{ID: 5, Type: "openai"}
	a := capabilityRankCacheKey(channel, []int{3, 1, 2}, "m", []model.Capability{model.CapabilityTool, model.CapabilityVision}, "https://e")
	b := capabilityRankCacheKey(channel, []int{2, 3, 1}, "m", []model.Capability{model.CapabilityVision, model.CapabilityTool}, "https://e")
	if a != b {
		t.Fatalf("key 顺序与 capability 顺序不应影响缓存键:\n%s\n%s", a, b)
	}
}

func seedCapabilityEvidence(t testing.TB, channel *model.Channel, keyID int, status model.CapabilityStatus) {
	t.Helper()
	now := time.Now().UTC()
	evidence := &model.CapabilityEvidence{
		ChannelID:           channel.ID,
		ChannelKeyID:        keyID,
		Model:               "probe-model",
		WireProtocol:        channel.Type,
		Capability:          model.CapabilityTool,
		Status:              status,
		ScopeFingerprint:    model.CapabilityScopeFingerprint(channel, channel.Keys[0], "https://upstream.test/v1"),
		Endpoint:            "https://upstream.test/v1",
		EndpointFingerprint: model.CapabilityEndpointFingerprint("https://upstream.test/v1"),
		ProbedAt:            now,
		ExpiresAt:           now.Add(time.Hour),
	}
	if err := CapabilityEvidenceUpsert(context.Background(), evidence); err != nil {
		t.Fatalf("CapabilityEvidenceUpsert: %v", err)
	}
}

// TestCapabilityRankReflectsEvidenceUpdateImmediately 锁定精确失效语义：
// evidence 变更（版本递增）必须立即反映到 rank，不等 TTL。
func TestCapabilityRankReflectsEvidenceUpdateImmediately(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "rank-cache.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	capabilityRankCache.clear()
	t.Cleanup(capabilityRankCache.clear)

	channel := &model.Channel{
		ID:   1,
		Type: "openai",
		Keys: []model.ChannelKey{{ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "sk-test"}},
	}
	required := []model.Capability{model.CapabilityTool}
	endpoint := "https://upstream.test/v1"

	seedCapabilityEvidence(t, channel, 11, model.CapabilitySupported)
	ranks, ok := capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint)
	if !ok || ranks[11] != 0 {
		t.Fatalf("supported evidence 应得 rank 0, got %v ok=%v", ranks, ok)
	}
	// 第二次调用走缓存，结果一致。
	ranks, ok = capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint)
	if !ok || ranks[11] != 0 {
		t.Fatalf("缓存命中结果应一致, got %v ok=%v", ranks, ok)
	}

	// evidence 翻转为 unauthorized：Upsert 递增版本，rank 必须立即变为 2。
	seedCapabilityEvidence(t, channel, 11, model.CapabilityUnauthorized)
	ranks, ok = capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint)
	if !ok || ranks[11] != 2 {
		t.Fatalf("evidence 变更后 rank 应立即更新为 2（版本失效）, got %v ok=%v", ranks, ok)
	}
}

// BenchmarkCapabilityKeyRanks 是 P1-3 的度量基线：cold 子基准模拟修复前
// 行为（每次调用一次 DB 查询），warm 子基准为修复后稳态（缓存命中）。
func BenchmarkCapabilityKeyRanks(b *testing.B) {
	if err := db.InitDB("sqlite", filepath.Join(b.TempDir(), "rank-bench.db"), false); err != nil {
		b.Fatalf("InitDB: %v", err)
	}
	capabilityRankCache.clear()
	b.Cleanup(capabilityRankCache.clear)

	channel := &model.Channel{
		ID:   1,
		Type: "openai",
		Keys: []model.ChannelKey{{ID: 11, ChannelID: 1, Enabled: true, ChannelKey: "sk-test"}},
	}
	required := []model.Capability{model.CapabilityTool}
	endpoint := "https://upstream.test/v1"
	seedCapabilityEvidence(b, channel, 11, model.CapabilitySupported)

	b.Run("cold-db-query", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			capabilityRankCache.clear()
			if _, ok := capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint); !ok {
				b.Fatal("rank lookup failed")
			}
		}
	})
	b.Run("warm-cache-hit", func(b *testing.B) {
		capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, ok := capabilityKeyRanks(context.Background(), channel, channel.Keys, "probe-model", required, endpoint); !ok {
				b.Fatal("rank lookup failed")
			}
		}
	})
}
