package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestRecordSuccessfulChannelBaselinePersistsOnlyWhenEnabled(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "relay-baseline.db"), false); err != nil {
		t.Fatalf("init database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	originalConfig := conf.Current()
	t.Cleanup(func() {
		if err := conf.Set(originalConfig); err != nil {
			t.Errorf("restore config: %v", err)
		}
	})
	channel := &model.Channel{ID: 51, Type: llm.APIFormatOpenAIResponse, RawPassthrough: true, SelfHealingEnabled: true}
	key := model.ChannelKey{ID: 52, ChannelID: 51, Enabled: true, ChannelKey: "account-secret"}
	artifact := requestartifact.Build(&httpclient.Request{
		Method:      http.MethodPost,
		URL:         "https://provider.test/v1/responses",
		Headers:     http.Header{"Authorization": {"Bearer account-secret"}, "Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"model":"model-a","input":"private prompt"}`),
	}, string(channel.Type), "model-a", requestartifact.RewriteSummary{RawPassthrough: true})
	ra := &relayAttempt{
		relayRun: &relayRun{metrics: &RelayMetrics{ActualModel: "model-a", OutboundRequestArtifact: artifact}},
		channel:  channel,
		usedKey:  key,
		baseURL:  "https://provider.test",
	}

	ra.recordSuccessfulChannelBaseline(context.Background(), http.StatusOK, http.Header{"Content-Type": {"application/json"}})
	items, err := op.ChannelBaselineList(context.Background(), channel.ID, 10)
	if err != nil {
		t.Fatalf("list disabled baselines: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("disabled capture persisted %d baselines", len(items))
	}

	config := originalConfig
	config.SelfHealing.Enabled = true
	config.SelfHealing.CaptureSuccessBaselines = true
	if err := conf.Set(config); err != nil {
		t.Fatalf("enable baseline capture: %v", err)
	}
	ra.recordSuccessfulChannelBaseline(context.Background(), http.StatusOK, http.Header{"Content-Type": {"application/json"}})
	items, err = waitForBaselines(context.Background(), channel.ID, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("enabled baselines = %#v, err=%v", items, err)
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	for _, secret := range []string{"account-secret", "private prompt"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("baseline leaked %q: %s", secret, encoded)
		}
	}
}

// waitForBaselines polls for the asynchronous baseline writer to land rows.
func waitForBaselines(ctx context.Context, channelID int, want int) ([]model.ChannelBaseline, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		items, err := op.ChannelBaselineList(ctx, channelID, 10)
		if err != nil || len(items) >= want || time.Now().After(deadline) {
			return items, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
