package routingstate

import "sync"

// Snapshot represents one published routing configuration generation. Changed
// is closed when a newer generation is published, allowing request paths to
// react immediately without polling.
type Snapshot struct {
	Revision uint64
	Changed  <-chan struct{}
}

type broadcaster struct {
	mu       sync.Mutex
	revision uint64
	changed  chan struct{}
}

var global = broadcaster{changed: make(chan struct{})}

// Current returns a race-free revision and notification channel pair.
func Current() Snapshot {
	global.mu.Lock()
	defer global.mu.Unlock()
	return Snapshot{Revision: global.revision, Changed: global.changed}
}

// Notify publishes a new routing generation and wakes every waiter attached to
// the previous generation.
func Notify() uint64 {
	global.mu.Lock()
	previous := global.changed
	global.revision++
	global.changed = make(chan struct{})
	revision := global.revision
	close(previous)
	global.mu.Unlock()
	return revision
}
