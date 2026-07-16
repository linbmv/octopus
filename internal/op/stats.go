package op

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
)

var statsService = NewStatsService()

type dirtySet struct {
	mu      sync.Mutex
	version uint64
	items   map[int]uint64
}

func newDirtySet() *dirtySet {
	return &dirtySet{items: make(map[int]uint64)}
}

func (d *dirtySet) mark(id int) {
	d.mu.Lock()
	d.version++
	d.items[id] = d.version
	d.mu.Unlock()
}

func (d *dirtySet) snapshot() map[int]uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	snap := make(map[int]uint64, len(d.items))
	for id, version := range d.items {
		snap[id] = version
	}
	return snap
}

func (d *dirtySet) clearUnchanged(snap map[int]uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for id, version := range snap {
		if d.items[id] == version {
			delete(d.items, id)
		}
	}
}

func (d *dirtySet) delete(id int) {
	d.mu.Lock()
	delete(d.items, id)
	d.mu.Unlock()
}

func (d *dirtySet) reset() {
	d.mu.Lock()
	d.version = 0
	d.items = make(map[int]uint64)
	d.mu.Unlock()
}

func dirtyIDs(snap map[int]uint64) []int {
	ids := make([]int, 0, len(snap))
	for id := range snap {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func statsLockFor(locks *[256]sync.Mutex, id int) *sync.Mutex {
	return &locks[uint(id)&255]
}

type StatsService struct {
	daily   model.StatsDaily
	dailyMu sync.RWMutex

	total   model.StatsTotal
	totalMu sync.RWMutex

	hourly   [24]model.StatsHourly
	hourlyMu sync.RWMutex

	channels           cache.Cache[int, model.StatsChannel]
	dirtyChannels      *dirtySet
	channelUpdateLocks [256]sync.Mutex

	apiKeys           cache.Cache[int, model.StatsAPIKey]
	dirtyAPIKeys      *dirtySet
	apiKeyUpdateLocks [256]sync.Mutex
	// apiKeyRequests is sharded by the same index as apiKeyUpdateLocks.  Every
	// access must hold the corresponding update lock so cost reads, actual cost
	// updates, request reservations, key updates, and deletion share one
	// linearization point.
	apiKeyRequests [256]map[int]apiKeyRequestState

	pendingDailyMu     sync.Mutex
	pendingDaily       map[string]model.StatsDaily
	pendingDailyNotify chan struct{}

	workerMu     sync.Mutex
	workerCancel context.CancelFunc
	workerDone   chan struct{}
	saveFailures atomic.Uint64
}

func NewStatsService() *StatsService {
	return &StatsService{
		channels:           cache.New[int, model.StatsChannel](16),
		dirtyChannels:      newDirtySet(),
		apiKeys:            cache.New[int, model.StatsAPIKey](16),
		dirtyAPIKeys:       newDirtySet(),
		pendingDaily:       make(map[string]model.StatsDaily),
		pendingDailyNotify: make(chan struct{}, 1),
	}
}

func (s *StatsService) totalSnapshot() model.StatsTotal {
	s.totalMu.RLock()
	defer s.totalMu.RUnlock()
	return s.total
}

func (s *StatsService) dailySnapshot() model.StatsDaily {
	s.dailyMu.RLock()
	defer s.dailyMu.RUnlock()
	return s.daily
}

func (s *StatsService) hourlySnapshot() [24]model.StatsHourly {
	s.hourlyMu.RLock()
	defer s.hourlyMu.RUnlock()
	return s.hourly
}

func (s *StatsService) markDirtyChannel(id int) {
	s.dirtyChannels.mark(id)
}

func (s *StatsService) markDirtyAPIKey(id int) {
	s.dirtyAPIKeys.mark(id)
}

func (s *StatsService) takeDirtyChannels() []int {
	return dirtyIDs(s.dirtyChannels.snapshot())
}

func (s *StatsService) takeDirtyAPIKeys() []int {
	return dirtyIDs(s.dirtyAPIKeys.snapshot())
}
