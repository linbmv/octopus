package op

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

// capabilityKeyRanks 是候选迭代热路径上唯一的数据库查询：故障风暴中每个请求
// 会对每个多 key 渠道各查一次 evidence。此进程内缓存把稳态查询降为内存命中。
//
// 失效双保险：
//  1. 任何 evidence 写入/删除都递增 capabilityEvidenceVersion（精确失效）。
//     删除发生在渠道更新事务内，版本在提交前递增——并发请求最多把旧数据
//     以新版本缓存一个 TTL 窗口，由 TTL 兜底。
//  2. 短 TTL 兜底 evidence 自身 expires_at 随时间推移导致的排序变化。
const (
	capabilityRankCacheTTL        = 5 * time.Second
	capabilityRankCacheMaxEntries = 4096
)

var (
	capabilityEvidenceVersion atomic.Uint64
	capabilityRankCache       = newRankCache()
)

func bumpCapabilityEvidenceVersion() { capabilityEvidenceVersion.Add(1) }

type rankCacheEntry struct {
	// ranks 在多个请求间共享，只读；禁止调用方修改。
	ranks     map[int]int
	version   uint64
	expiresAt time.Time
}

type rankCache struct {
	mu      sync.Mutex
	entries map[string]rankCacheEntry
}

func newRankCache() *rankCache {
	return &rankCache{entries: make(map[string]rankCacheEntry)}
}

func (c *rankCache) get(key string, version uint64, now time.Time) (map[int]int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if entry.version != version || now.After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.ranks, true
}

func (c *rankCache) put(key string, ranks map[int]int, version uint64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= capabilityRankCacheMaxEntries {
		// rank 重建成本低（一次索引查询），满时整体清空比逐条淘汰更简单可靠。
		c.entries = make(map[string]rankCacheEntry)
	}
	c.entries[key] = rankCacheEntry{ranks: ranks, version: version, expiresAt: now.Add(capabilityRankCacheTTL)}
}

func (c *rankCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]rankCacheEntry)
}

// capabilityRankCacheKey 唯一确定一次 rank 计算的输入。scope fingerprint 不入键：
// 影响 fingerprint 的渠道/key 变更必然伴随 evidence 删除（版本失效已覆盖）。
func capabilityRankCacheKey(channel *model.Channel, keyIDs []int, modelName string, required []model.Capability, endpoint string) string {
	ids := append([]int(nil), keyIDs...)
	sort.Ints(ids)
	caps := make([]string, 0, len(required))
	for _, capability := range required {
		caps = append(caps, string(capability))
	}
	sort.Strings(caps)

	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%s|", channel.ID, channel.Type, strings.TrimSpace(modelName), model.CapabilityEndpointFingerprint(endpoint))
	for _, id := range ids {
		fmt.Fprintf(&b, "%d,", id)
	}
	b.WriteByte('|')
	b.WriteString(strings.Join(caps, ","))
	return b.String()
}
