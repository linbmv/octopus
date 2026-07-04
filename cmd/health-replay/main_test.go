package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/relay/health"
)

func TestRunReplayParsesJSONLAndWritesReport(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "traffic.jsonl")
	lines := "" +
		`{"timestamp":"2026-07-04T00:00:00Z","channel_id":1,"key_id":1,"model":"m","first_token_ms":1000,"status_code":200}` + "\n" +
		`{"timestamp":"2026-07-04T00:00:01Z","channel_id":1,"key_id":1,"model":"m","first_token_ms":1200,"status_code":200}` + "\n" +
		`{"timestamp":"2026-07-04T00:00:02Z","channel_id":2,"key_id":1,"model":"m","first_token_ms":0,"status_code":504,"error":"timeout"}` + "\n"
	if err := os.WriteFile(logFile, []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := runReplay(ReplayConfig{
		LogFile:      logFile,
		OutputDir:    filepath.Join(dir, "out"),
		Algorithm:    "adaptive",
		MinRequests:  1,
		HealthConfig: health.DefaultHealthConfig(),
	})
	if err != nil {
		t.Fatalf("runReplay() error = %v", err)
	}
	if result.TotalEvents != 3 || result.OracleSuccess != 2 || result.CoveredEvents != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := saveReport(filepath.Join(dir, "out"), result); err != nil {
		t.Fatalf("saveReport() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "replay-report.md")); err != nil {
		t.Fatalf("expected markdown report: %v", err)
	}
}

func TestParseLogFileTimeFilter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "traffic.jsonl")
	lines := "" +
		`{"timestamp":"2026-07-04T00:00:00Z","channel_id":1,"key_id":1,"model":"m","first_token_ms":1000,"status_code":200}` + "\n" +
		`{"timestamp":"2026-07-05T00:00:00Z","channel_id":1,"key_id":1,"model":"m","first_token_ms":1000,"status_code":200}` + "\n"
	if err := os.WriteFile(logFile, []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}
	events, err := parseLogFile(logFile, "2026-07-04T12:00:00Z", "")
	if err != nil {
		t.Fatalf("parseLogFile() error = %v", err)
	}
	if len(events) != 1 || !events[0].Timestamp.Equal(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("events = %+v", events)
	}
}
