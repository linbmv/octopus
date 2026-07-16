package capability

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/looplj/axonhub/llm"
)

type boundedFakeProber struct {
	active    atomic.Int32
	maxActive atomic.Int32
	calls     chan Job
}

func (p *boundedFakeProber) Probe(
	_ context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint, modelName string,
	capability model.Capability,
	_ int,
) ProbeResult {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		current := p.maxActive.Load()
		if active <= current || p.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	p.calls <- Job{ChannelID: channel.ID, ChannelKeyID: key.ID, Endpoint: endpoint, Model: modelName, Capability: capability}
	return ProbeResult{Status: model.CapabilitySupported, HTTPStatus: 200}
}

func TestWorkerEnforcesCostCapConcurrencyAndPersistsTTL(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "capability-worker.db"), false); err != nil {
		t.Fatalf("init database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := op.InitCache(); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	channel := model.Channel{
		Name: "probe-worker", Type: llm.APIFormatOpenAIChatCompletion, Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://provider.test"}}, Model: "model-a",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "account-key"}},
	}
	if err := op.ChannelCreate(&channel, context.Background()); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	fake := &boundedFakeProber{calls: make(chan Job, 4)}
	config := conf.CapabilityProbe{
		Enabled: true, TTLSeconds: 3600, RequestsPerMinute: 6000,
		MaxConcurrency: 2, QueueDepth: 8, TimeoutSeconds: 5, MaxOutputTokens: 8,
		CostPerProbeUSD: 0.01, MaxBatchCostUSD: 0.02, MaxTotalCostUSD: 0.03,
	}
	worker, err := NewWorker(config, fake)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	if err := worker.Start(workerCtx); err != nil {
		cancel()
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = worker.Stop(stopCtx)
	})

	report, err := worker.Submit(context.Background(), SubmitRequest{
		ChannelID: channel.ID,
		Capabilities: []model.Capability{
			model.CapabilityText, model.CapabilityStream, model.CapabilityTool, model.CapabilityVision,
		},
	})
	if err != nil {
		t.Fatalf("submit probes: %v", err)
	}
	if report.Requested != 4 || report.Accepted != 2 || report.BudgetRejected != 2 || report.ReservedCostUSD != 0.02 {
		t.Fatalf("cost-bounded report = %#v", report)
	}
	for i := 0; i < report.Accepted; i++ {
		select {
		case <-fake.calls:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for bounded probe worker")
		}
	}
	if fake.maxActive.Load() > int32(config.MaxConcurrency) {
		t.Fatalf("max active probes = %d, limit = %d", fake.maxActive.Load(), config.MaxConcurrency)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		evidence, listErr := op.CapabilityEvidenceList(context.Background(), channel.ID)
		if listErr != nil {
			t.Fatalf("list evidence: %v", listErr)
		}
		if len(evidence) == report.Accepted {
			for _, item := range evidence {
				lifetime := item.ExpiresAt.Sub(item.ProbedAt)
				if lifetime < 3599*time.Second || lifetime > 3601*time.Second {
					t.Fatalf("evidence lifetime = %v, want configured TTL", lifetime)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted evidence count did not reach %d", report.Accepted)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDisabledWorkerRejectsBeforeCreatingPaidJobs(t *testing.T) {
	config := conf.Default().CapabilityProbe
	worker, err := NewWorker(config, &boundedFakeProber{calls: make(chan Job, 1)})
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	if _, err := worker.Submit(context.Background(), SubmitRequest{ChannelID: 1}); err != ErrProbeDisabled {
		t.Fatalf("disabled worker error = %v, want ErrProbeDisabled", err)
	}
}
