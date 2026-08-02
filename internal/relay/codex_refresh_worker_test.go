package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
)

func TestCodexOAuthRefreshWorkerPlansFromCredentialExpiration(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	worker := newCodexOAuthRefreshWorker()
	worker.reconcileInterval = time.Minute
	worker.refreshBefore = 5 * time.Minute

	channels := []dbmodel.Channel{
		codexRefreshTestChannel(1, true, codexRefreshTestKey(11, true, now.Add(4*time.Minute))),
		codexRefreshTestChannel(2, true, codexRefreshTestKey(12, true, now.Add(20*time.Minute))),
		codexRefreshTestChannel(3, false, codexRefreshTestKey(13, true, now.Add(time.Minute))),
		codexRefreshTestChannel(4, true, codexRefreshTestKey(14, false, now.Add(time.Minute))),
	}

	targets, delay := worker.plan(channels, now)
	if len(targets) != 1 || targets[0].channel.ID != 1 || targets[0].key.ID != 11 {
		t.Fatalf("targets = %#v, want enabled near-expiry key 11 only", targets)
	}
	if delay != time.Minute {
		t.Fatalf("delay = %s, want reconciliation interval %s", delay, time.Minute)
	}
}

func TestCodexOAuthRefreshWorkerRefreshesUnknownExpirationImmediately(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	worker := newCodexOAuthRefreshWorker()
	channel := codexRefreshTestChannel(1, true, dbmodel.ChannelKey{
		ID: 11, ChannelID: 1, Enabled: true,
		ChannelKey: `{"type":"codex","access_token":"opaque","refresh_token":"refresh"}`,
	})
	targets, _ := worker.plan([]dbmodel.Channel{channel}, now)
	if len(targets) != 1 || targets[0].key.ID != 11 {
		t.Fatalf("targets = %#v, want unknown-expiry credential to refresh immediately", targets)
	}
}

func TestCodexOAuthRefreshWorkerBacksOffFailedCredential(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	worker := newCodexOAuthRefreshWorker()
	worker.retryInterval = 5 * time.Minute
	worker.refreshTimeout = time.Second
	worker.refresh = func(context.Context, *dbmodel.Channel, dbmodel.ChannelKey, time.Duration) error {
		return errors.New("simulated refresh failure")
	}
	channel := codexRefreshTestChannel(1, true, codexRefreshTestKey(11, true, now.Add(time.Minute)))
	targets, _ := worker.plan([]dbmodel.Channel{channel}, now)
	worker.refreshTargets(context.Background(), targets, now)

	targets, delay := worker.plan([]dbmodel.Channel{channel}, now.Add(time.Minute))
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want failed credential in retry backoff", targets)
	}
	if delay != worker.reconcileInterval {
		t.Fatalf("delay = %s, want bounded reconciliation delay %s", delay, worker.reconcileInterval)
	}
}

func TestCodexOAuthRefreshWorkerStartStop(t *testing.T) {
	worker := newCodexOAuthRefreshWorker()
	worker.list = func(context.Context) ([]dbmodel.Channel, error) { return nil, nil }
	worker.reconcileInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func codexRefreshTestChannel(id int, enabled bool, keys ...dbmodel.ChannelKey) dbmodel.Channel {
	for i := range keys {
		keys[i].ChannelID = id
	}
	return dbmodel.Channel{
		ID: id, Type: dbmodel.ChannelTypeOpenAICodex, Enabled: enabled,
		BaseUrls: []dbmodel.BaseUrl{{URL: "https://chatgpt.com/backend-api/codex"}},
		Keys:     keys,
	}
}

func codexRefreshTestKey(id int, enabled bool, expiresAt time.Time) dbmodel.ChannelKey {
	return dbmodel.ChannelKey{
		ID: id, Enabled: enabled,
		ChannelKey: fmt.Sprintf(`{"type":"codex","access_token":"opaque","refresh_token":"refresh","expired":%q}`, expiresAt.Format(time.RFC3339)),
	}
}
