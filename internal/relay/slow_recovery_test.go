package relay

import (
	"sync"
	"testing"
	"time"
)

func TestSlowRecoveryUsesImmediateRetryThenBoundedBackoff(t *testing.T) {
	now := time.Unix(500, 0)
	tracker := newSlowRecoveryTracker(func() time.Time { return now })
	key := newSlowRecoveryKey(10, 11, "model", "https://one.example")

	tracker.recordTimeout(key, 0)
	allowed, lease, remaining := tracker.acquire(key, 30*time.Second)
	if !allowed || lease == 0 || remaining != 0 {
		t.Fatalf("first recovery = (%t, %d, %v), want an immediate lease", allowed, lease, remaining)
	}
	tracker.recordTimeout(key, lease)
	if allowed, _, remaining := tracker.acquire(key, time.Minute); allowed || remaining != time.Minute {
		t.Fatalf("second timeout backoff = (%t, %v), want 60s block", allowed, remaining)
	}

	now = now.Add(time.Minute)
	allowed, lease, _ = tracker.acquire(key, time.Minute)
	if !allowed || lease == 0 {
		t.Fatal("due slow candidate was not admitted")
	}
	tracker.recordTimeout(key, lease)
	if entry := tracker.entries[key]; entry.timeouts != 3 || entry.nextAttempt.Sub(now) != 2*time.Minute {
		t.Fatalf("third timeout entry = %+v, want 120s backoff", entry)
	}
	tracker.recordSuccess(key, 0)
	if _, exists := tracker.entries[key]; exists {
		t.Fatal("successful responsive outcome did not clear slow state")
	}
}

func TestSlowRecoveryAllowsOnlyOneConcurrentLease(t *testing.T) {
	now := time.Unix(600, 0)
	tracker := newSlowRecoveryTracker(func() time.Time { return now })
	key := newSlowRecoveryKey(20, 21, "model", "https://one.example")
	tracker.recordTimeout(key, 0)

	var wg sync.WaitGroup
	var mu sync.Mutex
	leases := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if allowed, lease, _ := tracker.acquire(key, time.Minute); allowed && lease != 0 {
				mu.Lock()
				leases++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if leases != 1 {
		t.Fatalf("concurrent slow recovery leases = %d, want exactly 1", leases)
	}
}

func TestSlowRecoveryIgnoresStaleLeaseCompletion(t *testing.T) {
	now := time.Unix(700, 0)
	tracker := newSlowRecoveryTracker(func() time.Time { return now })
	tracker.maxBackoff = 10 * time.Minute
	key := newSlowRecoveryKey(30, 31, "model", "https://one.example")
	tracker.recordTimeout(key, 0)
	firstAllowed, firstLease, _ := tracker.acquire(key, time.Second)
	if !firstAllowed || firstLease == 0 {
		t.Fatal("failed to acquire initial lease")
	}
	now = now.Add(2 * time.Second)
	secondAllowed, secondLease, _ := tracker.acquire(key, time.Second)
	if !secondAllowed || secondLease == firstLease {
		t.Fatalf("expired lease was not replaced: first=%d second=%d", firstLease, secondLease)
	}
	tracker.recordTimeout(key, firstLease)
	entry := tracker.entries[key]
	if entry.leaseID != secondLease || entry.timeouts != 1 {
		t.Fatalf("stale timeout replaced current lease: %+v", entry)
	}
	tracker.release(key, firstLease)
	if tracker.entries[key].leaseID != secondLease {
		t.Fatal("stale release replaced current lease")
	}
	tracker.recordSuccess(key, secondLease)
}

func TestSlowRecoveryIdentityDoesNotContainCredential(t *testing.T) {
	key := newSlowRecoveryKey(40, 41, "model", "https://one.example")
	if key.channelID != 40 || key.keyID != 41 || key.model != "model" || key.baseURL != "https://one.example" {
		t.Fatalf("unexpected slow identity: %+v", key)
	}
}
