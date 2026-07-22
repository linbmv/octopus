package model

import "testing"

func TestStatsMetricsAddIncludesReasoningTokens(t *testing.T) {
	stats := StatsMetrics{InputToken: 10, OutputToken: 8, ReasoningToken: 3}
	stats.Add(StatsMetrics{InputToken: 4, OutputToken: 6, ReasoningToken: 5})

	if stats.InputToken != 14 || stats.OutputToken != 14 || stats.ReasoningToken != 8 {
		t.Fatalf("aggregated token metrics = %#v", stats)
	}
}
