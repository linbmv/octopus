package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func TestSlowRecoveryTrackerUsesImmediateFirstRetryAndBoundedLease(t *testing.T) {
	tracker := newSlowRecoveryTracker()
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	key := slowRecoveryKey{
		ChannelID: 1, ChannelKeyID: 2, Model: "model-a", ConfigVersion: 3, ScopeFingerprint: "scope-a",
	}

	if allowed, lease, remaining := tracker.acquire(key); !allowed || lease != 0 || remaining != 0 {
		t.Fatalf("unknown candidate acquire = (%t, %d, %v), want (true, 0, 0)", allowed, lease, remaining)
	}

	tracker.recordTimeout(key, 150*time.Second)
	entry := tracker.entries[key]
	if entry.ConsecutiveTimeouts != 1 || !entry.NextAttempt.Equal(now) {
		t.Fatalf("first timeout entry = %+v, want immediate passive retry", entry)
	}
	if entry.LastBudget != 120*time.Second {
		t.Fatalf("recorded budget = %v, want hard-capped 120s", entry.LastBudget)
	}

	allowed, lease, remaining := tracker.acquire(key)
	if !allowed || lease == 0 || remaining != 0 {
		t.Fatalf("first passive acquire = (%t, %d, %v), want leased", allowed, lease, remaining)
	}
	firstLease := lease
	entry = tracker.entries[key]
	if got := entry.LeaseUntil.Sub(now); got != 120*time.Second {
		t.Fatalf("passive lease = %v, want 120s", got)
	}
	if allowed, lease, remaining = tracker.acquire(key); allowed || lease != 0 || remaining != 120*time.Second {
		t.Fatalf("concurrent acquire = (%t, %d, %v), want blocked for 120s", allowed, lease, remaining)
	}

	tracker.release(key, firstLease)
	if allowed, lease, remaining = tracker.acquire(key); !allowed || lease == 0 || remaining != 0 {
		t.Fatalf("released acquire = (%t, %d, %v), want leased", allowed, lease, remaining)
	}
	tracker.recordTimeoutForLease(key, 120*time.Second, lease)
	entry = tracker.entries[key]
	secondBackoff := entry.NextAttempt.Sub(now)
	if entry.ConsecutiveTimeouts != 2 || secondBackoff != time.Minute {
		t.Fatalf("second timeout entry = %+v, want 60s backoff", entry)
	}
	if allowed, lease, remaining = tracker.acquire(key); allowed || lease != 0 || remaining != time.Minute {
		t.Fatalf("backoff acquire = (%t, %d, %v), want blocked for 60s", allowed, lease, remaining)
	}

	now = now.Add(time.Minute)
	if allowed, lease, remaining = tracker.acquire(key); !allowed || lease == 0 || remaining != 0 {
		t.Fatalf("due acquire = (%t, %d, %v), want leased", allowed, lease, remaining)
	}
	tracker.recordTimeoutForLease(key, 120*time.Second, lease)
	entry = tracker.entries[key]
	thirdBackoff := entry.NextAttempt.Sub(now)
	if entry.ConsecutiveTimeouts != 3 || thirdBackoff != 2*time.Minute {
		t.Fatalf("third timeout entry = %+v, want 120s backoff", entry)
	}

	tracker.recordSuccess(key)
	if _, exists := tracker.entries[key]; exists {
		t.Fatal("successful passive recovery did not clear slow state")
	}
	if allowed, lease, remaining = tracker.acquire(key); !allowed || lease != 0 || remaining != 0 {
		t.Fatalf("post-success acquire = (%t, %d, %v), want ordinary candidate", allowed, lease, remaining)
	}
}

func TestSlowRecoveryTrackerAllowsOnlyOneConcurrentPassiveAttempt(t *testing.T) {
	tracker := newSlowRecoveryTracker()
	tracker.recordTimeout(slowRecoveryKey{ChannelID: 10, ChannelKeyID: 11, Model: "m", ScopeFingerprint: "scope"}, 30*time.Second)
	key := slowRecoveryKey{ChannelID: 10, ChannelKeyID: 11, Model: "m", ScopeFingerprint: "scope"}

	var leases atomic.Int64
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if allowed, lease, _ := tracker.acquire(key); allowed && lease != 0 {
				leases.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	if got := leases.Load(); got != 1 {
		t.Fatalf("concurrent passive leases = %d, want exactly 1", got)
	}
}

func TestSlowRecoveryTrackerRejectsStaleLeaseCompletion(t *testing.T) {
	tracker := newSlowRecoveryTracker()
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	tracker.lease = time.Second
	key := slowRecoveryKey{ChannelID: 10, ChannelKeyID: 11, Model: "m", ScopeFingerprint: "scope"}
	tracker.recordTimeout(key, 30*time.Second)

	_, staleLease, _ := tracker.acquire(key)
	now = now.Add(2 * time.Second)
	_, currentLease, _ := tracker.acquire(key)
	if staleLease == 0 || currentLease == 0 || staleLease == currentLease {
		t.Fatalf("lease generations = stale:%d current:%d, want distinct non-zero IDs", staleLease, currentLease)
	}

	tracker.release(key, staleLease)
	entry := tracker.entries[key]
	if !entry.InFlight || entry.LeaseID != currentLease {
		t.Fatalf("stale release replaced current lease: %+v", entry)
	}
	tracker.recordTimeoutForLease(key, 120*time.Second, staleLease)
	entry = tracker.entries[key]
	if entry.ConsecutiveTimeouts != 1 || !entry.InFlight || entry.LeaseID != currentLease {
		t.Fatalf("stale timeout completion mutated current state: %+v", entry)
	}
	tracker.release(key, currentLease)
	entry = tracker.entries[key]
	if entry.InFlight || entry.LeaseID != 0 {
		t.Fatalf("current lease release did not clear ownership: %+v", entry)
	}
}

func TestSlowRecoveryIdentityIsScopedWithoutRetainingCredentials(t *testing.T) {
	channel := &dbmodel.Channel{ID: 21, ConfigVersion: 4, Type: llm.APIFormatOpenAIChatCompletion}
	key := dbmodel.ChannelKey{ID: 22, ChannelKey: "secret-credential"}
	first := newSlowRecoveryKey(channel, key, "m", "https://user:password@example.com/v1?route=one&token=hidden")
	second := newSlowRecoveryKey(channel, key, "m", "https://user:password@example.com/v1?route=two&token=hidden")
	changedKey := newSlowRecoveryKey(channel, dbmodel.ChannelKey{ID: 22, ChannelKey: "replacement-credential"}, "m", "https://user:password@example.com/v1?route=one&token=hidden")

	if first.ScopeFingerprint == "" || len(first.ScopeFingerprint) != 64 {
		t.Fatalf("scope fingerprint = %q, want SHA-256 identity", first.ScopeFingerprint)
	}
	for _, secret := range []string{"secret-credential", "password", "token", "route"} {
		if strings.Contains(first.ScopeFingerprint, secret) {
			t.Fatalf("scope fingerprint retained sensitive source text %q", secret)
		}
	}
	if first == second {
		t.Fatal("different endpoint query scopes shared one slow-recovery identity")
	}
	if first == changedKey {
		t.Fatal("replaced credential shared one slow-recovery identity")
	}
}

func TestSlowRecoveryTrackerInvalidationAndStateBound(t *testing.T) {
	tracker := newSlowRecoveryTracker()
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	tracker.maxEntries = 2
	first := slowRecoveryKey{ChannelID: 1, ChannelKeyID: 1, Model: "m", ScopeFingerprint: "one"}
	second := slowRecoveryKey{ChannelID: 1, ChannelKeyID: 2, Model: "other", ScopeFingerprint: "two"}
	third := slowRecoveryKey{ChannelID: 2, ChannelKeyID: 3, Model: "m", ScopeFingerprint: "three"}

	tracker.recordTimeout(first, 30*time.Second)
	now = now.Add(time.Second)
	tracker.recordTimeout(second, 30*time.Second)
	now = now.Add(time.Second)
	tracker.recordTimeout(third, 30*time.Second)
	if len(tracker.entries) != tracker.maxEntries {
		t.Fatalf("slow state size = %d, want bound %d", len(tracker.entries), tracker.maxEntries)
	}
	if _, exists := tracker.entries[first]; exists {
		t.Fatal("oldest slow state was not evicted")
	}

	tracker.invalidateChannel(1, "other")
	if _, exists := tracker.entries[second]; exists {
		t.Fatal("model-scoped channel invalidation retained matching state")
	}
	if _, exists := tracker.entries[third]; !exists {
		t.Fatal("channel invalidation removed unrelated channel state")
	}
	tracker.invalidateAll()
	if len(tracker.entries) != 0 {
		t.Fatalf("global invalidation retained %d slow entries", len(tracker.entries))
	}
}

func TestSlowRecoveryAttemptClearsOnlyOnResponsiveOutcome(t *testing.T) {
	tracker := useIsolatedSlowRecoveryTracker(t)
	channel := passiveRecoveryTestChannel(72001)
	credential := dbmodel.ChannelKey{ID: 73001, ChannelID: channel.ID, Enabled: true, ChannelKey: "test-key"}
	key := newSlowRecoveryKey(channel, credential, "m", channel.BaseUrls[0].URL)
	tracker.recordTimeout(key, 30*time.Second)
	allowed, lease, _ := tracker.acquire(key)
	if !allowed || lease == 0 {
		t.Fatal("failed to acquire slow-recovery test lease")
	}

	attempt := &relayAttempt{channel: channel, usedKey: credential, baseURL: channel.BaseUrls[0].URL, slowRecoveryKey: key, slowRecoveryLease: lease}
	attempt.releaseSlowRecoveryLease()
	entry, exists := tracker.entries[key]
	if !exists || entry.InFlight {
		t.Fatalf("policy-only release state = (%+v, %t), want retained without lease", entry, exists)
	}

	allowed, lease, _ = tracker.acquire(key)
	if !allowed || lease == 0 {
		t.Fatal("failed to reacquire slow-recovery test lease")
	}
	attempt.slowRecoveryLease = lease
	attempt.clearSlowRecoveryState()
	if _, exists := tracker.entries[key]; exists {
		t.Fatal("responsive recovery outcome retained slow state")
	}
}

func TestSlowRecoveryTimeoutClassificationKeepsManualAndAdaptivePoliciesSeparate(t *testing.T) {
	for _, source := range []firstTokenTimeoutSource{
		firstTokenTimeoutGlobal,
		firstTokenTimeoutColdStart,
		firstTokenTimeoutNonStreamAttempt,
		firstTokenTimeoutBudget,
		firstTokenTimeoutChannelException,
	} {
		err := firstTokenTimeoutConfig{Duration: 30 * time.Second, Source: source}.
			Error(firstTokenTimeoutPhaseWaitingHeaders)
		if !isSlowRecoveryTimeout(err) {
			t.Fatalf("source %v did not enter passive slow recovery", source)
		}
	}
	for _, source := range []firstTokenTimeoutSource{firstTokenTimeoutManual, firstTokenTimeoutAdaptive} {
		err := firstTokenTimeoutConfig{Duration: 30 * time.Second, Source: source}.
			Error(firstTokenTimeoutPhaseWaitingHeaders)
		if isSlowRecoveryTimeout(err) {
			t.Fatalf("source %v incorrectly entered passive slow recovery", source)
		}
	}
	if !isSlowRecoveryTimeout(errNonStreamRequestTimeout) {
		t.Fatal("hard non-stream request timeout did not enter passive slow recovery")
	}
	if isSlowRecoveryTimeout(errors.New("connection refused")) {
		t.Fatal("ordinary transport failure entered passive slow recovery")
	}
	oversized := firstTokenTimeoutConfig{Duration: 150 * time.Second, Source: firstTokenTimeoutGlobal}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if got := slowRecoveryTimeoutBudget(oversized); got != 120*time.Second {
		t.Fatalf("oversized recovery budget = %v, want 120s", got)
	}
	exception := firstTokenTimeoutConfig{Duration: 200 * time.Second, Source: firstTokenTimeoutChannelException}.
		Error(firstTokenTimeoutPhaseWaitingHeaders)
	if got := slowRecoveryTimeoutBudget(exception); got != 200*time.Second {
		t.Fatalf("channel exception recovery budget = %v, want 200s", got)
	}
}

func TestSlowRecoveryExceptionLeaseUsesChannelBudget(t *testing.T) {
	tracker := newSlowRecoveryTracker()
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	key := slowRecoveryKey{ChannelID: 1, ChannelKeyID: 2, Model: "m", ScopeFingerprint: "scope"}
	tracker.recordTimeout(key, 30*time.Second)
	if allowed, lease, _ := tracker.acquireForBudget(key, 200*time.Second); !allowed || lease == 0 {
		t.Fatal("exception candidate did not receive a recovery lease")
	}
	if got := tracker.entries[key].LeaseUntil.Sub(now); got != 200*time.Second {
		t.Fatalf("exception recovery lease = %v, want 200s", got)
	}
}

func TestRelayRunDefersSlowCandidateUntilOrdinaryCandidatesFinish(t *testing.T) {
	for _, test := range []struct {
		name        string
		fastSuccess bool
		want        []int
	}{
		{name: "ordinary success does not probe slow candidate", fastSuccess: true, want: []int{72002}},
		{name: "ordinary failure uses slow candidate passively", fastSuccess: false, want: []int{72002, 72001}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker := useIsolatedSlowRecoveryTracker(t)
			run, slowKey := newPassiveRecoveryOrderingRun(t, tracker)
			tracker.recordTimeout(slowKey, 30*time.Second)

			attempted := make([]int, 0, 2)
			run.runAttemptFunc = func(attempt *relayAttempt) (bool, error) {
				attempted = append(attempted, attempt.channel.ID)
				if attempt.channel.ID == 72002 && !test.fastSuccess {
					return false, errors.New("ordinary candidate failed")
				}
				if attempt.channel.ID == 72001 {
					if attempt.selectionReason != "passive_slow_recovery" {
						t.Fatalf("slow selection reason = %q", attempt.selectionReason)
					}
					if len(attempt.keyOptions) != 2 || len(attempt.baseURLOptions) != 2 {
						t.Fatalf("deferred same-channel fallbacks = %d keys/%d URLs, want 2/2",
							len(attempt.keyOptions), len(attempt.baseURLOptions))
					}
					attempt.clearSlowRecoveryState()
				}
				return false, nil
			}

			run.run()
			if len(attempted) != len(test.want) {
				t.Fatalf("attempted channels = %v, want %v", attempted, test.want)
			}
			for i := range test.want {
				if attempted[i] != test.want[i] {
					t.Fatalf("attempted channels = %v, want %v", attempted, test.want)
				}
			}
			entry, exists := tracker.entries[slowKey]
			if test.fastSuccess {
				if !exists || entry.InFlight {
					t.Fatalf("unneeded slow recovery state = (%+v, %t), want retained without lease", entry, exists)
				}
			} else if exists {
				t.Fatalf("successful passive recovery retained state: %+v", entry)
			}
		})
	}
}

func useIsolatedSlowRecoveryTracker(t *testing.T) *slowRecoveryTracker {
	t.Helper()
	previous := globalSlowRecovery
	tracker := newSlowRecoveryTracker()
	globalSlowRecovery = tracker
	t.Cleanup(func() {
		globalSlowRecovery = previous
		balancer.InvalidateChannel(72001)
		balancer.InvalidateChannel(72002)
	})
	return tracker
}

func passiveRecoveryTestChannel(id int) *dbmodel.Channel {
	return &dbmodel.Channel{
		ID: id, Name: "channel", Type: llm.APIFormatOpenAIChatCompletion, Enabled: true, ConfigVersion: 1,
		BaseUrls: []dbmodel.BaseUrl{
			{URL: "https://provider.example/v1"},
			{URL: "https://provider-backup.example/v1"},
		},
	}
}

func newPassiveRecoveryOrderingRun(t *testing.T, tracker *slowRecoveryTracker) (*relayRun, slowRecoveryKey) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	items := []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 72001, ModelName: "m", Priority: 1},
		{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 72002, ModelName: "m", Priority: 2},
	}
	group := dbmodel.Group{Name: "m", Mode: dbmodel.GroupModeFailover, Items: items}
	iter := balancer.NewIterator(group, 1, "m")
	request := &llm.Request{Model: "m", APIFormat: llm.APIFormatOpenAIChatCompletion, RequestType: llm.RequestTypeChat}
	run := &relayRun{
		c: ginCtx, internalRequest: request,
		metrics: &RelayMetrics{StartTime: time.Now(), APIKeyID: 1, RequestModel: "m", ActualModel: "m"},
		group:   group, iter: iter,
		iterStack:     []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory:   []*balancer.Iterator{iter},
		failoverState: newRequestFailoverState(),
	}
	run.attachIteratorTimeline(iter)
	slowChannel := passiveRecoveryTestChannel(72001)
	slowCredential := dbmodel.ChannelKey{ID: 73001, ChannelID: 72001, Enabled: true, ChannelKey: "test-key"}
	slowKey := newSlowRecoveryKey(slowChannel, slowCredential, "m", slowChannel.BaseUrls[0].URL)

	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		channel := passiveRecoveryTestChannel(item.ChannelID)
		credential := dbmodel.ChannelKey{
			ID: item.ChannelID + 1000, ChannelID: item.ChannelID, Enabled: true, ChannelKey: "test-key",
		}
		keyOptions := []dbmodel.ChannelKey{
			credential,
			{ID: credential.ID + 1, ChannelID: item.ChannelID, Enabled: true, ChannelKey: "backup-test-key"},
		}
		identity := newSlowRecoveryKey(channel, credential, item.ModelName, channel.BaseUrls[0].URL)
		allowed, lease, _ := tracker.acquire(identity)
		if !allowed {
			return nil, nil
		}
		return &relayAttempt{
			relayRun: run, channel: channel, groupItem: item, usedKey: credential,
			keyOptions: keyOptions, keyIndex: 0,
			baseURL: channel.BaseUrls[0].URL,
			baseURLOptions: []string{
				channel.BaseUrls[0].URL,
				channel.BaseUrls[1].URL,
			},
			baseURLIndex:    0,
			slowRecoveryKey: identity, slowRecoveryLease: lease,
		}, nil
	}
	return run, slowKey
}
