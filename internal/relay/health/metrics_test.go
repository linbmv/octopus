package health

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestHealthMetricsUpdateSetsSnapshotCounts(t *testing.T) {
	metrics := NewHealthMetrics("octopus_test_metrics")
	health := NewChannelHealth(HealthKey{ChannelID: 1, KeyID: 2, Model: "gpt-4"}, DefaultHealthConfig())
	health.RestoreStats(HealthStats{
		TotalCount:                 10,
		SuccessCount:               7,
		TimeoutCount:               2,
		AutoFirstTokenTimeoutCount: 1,
		NetworkCount:               3,
		RateLimitCount:             4,
		ModelErrorCount:            5,
		KeyErrorCount:              6,
		RecentResults:              []bool{true, false},
		LastEventAt:                time.Now(),
	}, 0.5)

	key := HealthKey{ChannelID: 1, KeyID: 2, Model: "gpt-4"}
	metrics.Update(key, health)

	assertGaugeValue(t, metrics.totalRequests, 10, "total requests")
	assertGaugeValue(t, metrics.successRequests, 7, "success requests")
	assertGaugeValue(t, metrics.failedRequests, 3, "failed requests")
	assertGaugeValue(t, metrics.timeoutCount, 2, "timeout count")
	assertGaugeValue(t, metrics.autoFirstTokenTimeoutCount, 1, "auto first-token timeout count")
	assertGaugeValue(t, metrics.networkErrCount, 3, "network error count")
	assertGaugeValue(t, metrics.rateLimitCount, 4, "rate limit count")
	assertGaugeValue(t, metrics.modelErrCount, 5, "model error count")
	assertGaugeValue(t, metrics.keyErrCount, 6, "key error count")
}

func assertGaugeValue(t *testing.T, gauge *prometheus.GaugeVec, want float64, name string) {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.WithLabelValues("1", "2", "gpt-4").Write(metric); err != nil {
		t.Fatalf("failed to read %s: %v", name, err)
	}
	if metric.Gauge == nil || metric.Gauge.Value == nil {
		t.Fatalf("%s gauge value missing", name)
	}
	got := metric.Gauge.GetValue()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}
