package health

import (
	"errors"
	"math/rand"
	"sort"
	"sync"
	"time"
)

var ErrEmptyWeightTable = errors.New("empty health weight table")

type WeightCandidate struct {
	ChannelID int
	KeyID     int
	Model     string
	BaseWeight float64
}

type WeightedCandidate struct {
	WeightCandidate
	HealthScore float64
	Weight      float64
	CumSum      float64
}

type WeightTable struct {
	GroupKey   string
	Candidates []WeightedCandidate
	TotalW     float64
	Updated    time.Time
}

type WeightTableManager struct {
	mu      sync.RWMutex
	manager *HealthManager
	tables  map[string]*WeightTable
	now     func() time.Time
}

func NewWeightTableManager(manager *HealthManager) *WeightTableManager {
	return &WeightTableManager{
		manager: manager,
		tables:  make(map[string]*WeightTable),
		now:     time.Now,
	}
}

func (m *WeightTableManager) Refresh(groupKey string, candidates []WeightCandidate) error {
	next, err := m.Build(groupKey, candidates)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.tables[groupKey] = next
	m.mu.Unlock()
	return nil
}

func (m *WeightTableManager) Build(groupKey string, candidates []WeightCandidate) (*WeightTable, error) {
	if len(candidates) == 0 {
		return nil, ErrEmptyWeightTable
	}

	weighted := make([]WeightedCandidate, 0, len(candidates))
	total := 0.0
	for _, candidate := range candidates {
		baseWeight := candidate.BaseWeight
		if baseWeight <= 0 {
			baseWeight = 1
		}
		score := 1.0
		if m.manager != nil && m.manager.IsEnabled() {
			score = m.manager.GetScore(candidate.ChannelID, candidate.KeyID, candidate.Model)
		}
		weight := baseWeight * score
		if weight <= 0 {
			continue
		}
		total += weight
		weighted = append(weighted, WeightedCandidate{
			WeightCandidate: candidate,
			HealthScore:     score,
			Weight:          weight,
			CumSum:          total,
		})
	}
	if len(weighted) == 0 || total <= 0 {
		return nil, ErrEmptyWeightTable
	}
	return &WeightTable{GroupKey: groupKey, Candidates: weighted, TotalW: total, Updated: m.now()}, nil
}

func (m *WeightTableManager) Get(groupKey string) (*WeightTable, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	table, ok := m.tables[groupKey]
	return table, ok
}

func (m *WeightTableManager) Select(groupKey string, randomValue float64) (WeightedCandidate, bool) {
	table, ok := m.Get(groupKey)
	if !ok || table.TotalW <= 0 || len(table.Candidates) == 0 {
		return WeightedCandidate{}, false
	}
	if randomValue < 0 || randomValue >= 1 {
		randomValue = rand.Float64()
	}
	target := randomValue * table.TotalW
	idx := sort.Search(len(table.Candidates), func(i int) bool {
		return table.Candidates[i].CumSum > target
	})
	if idx >= len(table.Candidates) {
		idx = len(table.Candidates) - 1
	}
	return table.Candidates[idx], true
}

func (m *WeightTableManager) OnChannelDeleted(channelID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for groupKey, table := range m.tables {
		filtered := make([]WeightCandidate, 0, len(table.Candidates))
		for _, candidate := range table.Candidates {
			if candidate.ChannelID != channelID {
				filtered = append(filtered, candidate.WeightCandidate)
			}
		}
		if len(filtered) == 0 {
			delete(m.tables, groupKey)
			continue
		}
		next, err := m.Build(groupKey, filtered)
		if err == nil {
			m.tables[groupKey] = next
		}
	}
}

func (m *WeightTableManager) OnKeyDeleted(keyID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for groupKey, table := range m.tables {
		filtered := make([]WeightCandidate, 0, len(table.Candidates))
		for _, candidate := range table.Candidates {
			if candidate.KeyID != keyID {
				filtered = append(filtered, candidate.WeightCandidate)
			}
		}
		if len(filtered) == 0 {
			delete(m.tables, groupKey)
			continue
		}
		next, err := m.Build(groupKey, filtered)
		if err == nil {
			m.tables[groupKey] = next
		}
	}
}
