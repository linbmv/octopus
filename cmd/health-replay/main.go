package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/relay/health"
)

type ReplayConfig struct {
	LogFile       string
	OutputDir     string
	StartTime     string
	EndTime       string
	Algorithm     string
	MinRequests   int64
	HealthConfig  health.HealthConfig
}

type ReplayResult struct {
	TotalEvents       int64                 `json:"total_events"`
	CoveredEvents     int64                 `json:"covered_events"`
	OracleSuccess     int64                 `json:"oracle_success"`
	OracleFailure     int64                 `json:"oracle_failure"`
	AlgorithmSuccess  int64                 `json:"algorithm_success"`
	AlgorithmTimeout  int64                 `json:"algorithm_timeout"`
	FalsePositive     int64                 `json:"false_positive"`
	FalseNegative     int64                 `json:"false_negative"`
	FalsePositiveRate float64               `json:"false_positive_rate"`
	TimeoutRate       float64               `json:"timeout_rate"`
	RetryReduction    float64               `json:"retry_reduction"`
	P95Delta          float64               `json:"p95_delta"`
	ChannelResults    map[int]*ChannelReplayResult `json:"channel_results"`
}

type ChannelReplayResult struct {
	ChannelID         int     `json:"channel_id"`
	TotalEvents       int64   `json:"total_events"`
	OracleSuccess     int64   `json:"oracle_success"`
	AlgorithmSuccess  int64   `json:"algorithm_success"`
	AlgorithmTimeout  int64   `json:"algorithm_timeout"`
	FalsePositive     int64   `json:"false_positive"`
	FalsePositiveRate float64 `json:"false_positive_rate"`
}

type LogEvent struct {
	Timestamp      time.Time     `json:"timestamp"`
	ChannelID      int           `json:"channel_id"`
	KeyID          int           `json:"key_id"`
	Model          string        `json:"model"`
	FirstTokenTime time.Duration `json:"-"`
	FirstTokenMS   int64         `json:"first_token_ms"`
	StatusCode     int           `json:"status_code"`
	Error          string        `json:"error"`
}

func main() {
	config := parseFlags()
	result, err := runReplay(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay failed: %v\n", err)
		os.Exit(1)
	}
	printResult(result)
	if err := saveReport(config.OutputDir, result); err != nil {
		fmt.Fprintf(os.Stderr, "save report failed: %v\n", err)
		os.Exit(1)
	}
	if !passesAcceptance(result) {
		os.Exit(1)
	}
}

func parseFlags() ReplayConfig {
	config := ReplayConfig{HealthConfig: health.DefaultHealthConfig(), MinRequests: 100}
	flag.StringVar(&config.LogFile, "log", "", "JSONL log file path")
	flag.StringVar(&config.OutputDir, "output", "./replay-output", "output directory")
	flag.StringVar(&config.StartTime, "start", "", "start time (RFC3339)")
	flag.StringVar(&config.EndTime, "end", "", "end time (RFC3339)")
	flag.StringVar(&config.Algorithm, "algorithm", "adaptive", "algorithm: baseline or adaptive")
	flag.Int64Var(&config.MinRequests, "min-requests", 100, "minimum requests per channel for coverage")
	flag.DurationVar(&config.HealthConfig.MinTimeout, "min-timeout", 5*time.Second, "min timeout")
	flag.DurationVar(&config.HealthConfig.MaxTimeout, "max-timeout", 40*time.Second, "max timeout")
	flag.DurationVar(&config.HealthConfig.DefaultTimeout, "default-timeout", 15*time.Second, "default timeout")
	flag.Parse()
	if config.LogFile == "" {
		fmt.Fprintln(os.Stderr, "-log is required")
		flag.Usage()
		os.Exit(2)
	}
	return config
}

func runReplay(config ReplayConfig) (*ReplayResult, error) {
	manager := health.NewHealthManager(config.HealthConfig)
	events, err := parseLogFile(config.LogFile, config.StartTime, config.EndTime)
	if err != nil {
		return nil, err
	}
	result := &ReplayResult{ChannelResults: make(map[int]*ChannelReplayResult)}
	for _, event := range events {
		processEvent(manager, event, result, config.Algorithm)
	}
	finalizeResult(result, config.MinRequests)
	return result, nil
}

func processEvent(manager *health.HealthManager, event *LogEvent, result *ReplayResult, algorithm string) {
	result.TotalEvents++
	channelResult := getOrCreateChannelResult(result, event.ChannelID)
	channelResult.TotalEvents++

	isOracleSuccess := event.StatusCode >= 200 && event.StatusCode < 300 && strings.TrimSpace(event.Error) == ""
	if isOracleSuccess {
		result.OracleSuccess++
		channelResult.OracleSuccess++
	} else {
		result.OracleFailure++
	}

	timeout := manager.GetTimeout(event.ChannelID, event.KeyID, event.Model)
	if algorithm == "baseline" {
		timeout = manager.GetTimeout(0, 0, "")
	}
	isAlgorithmSuccess := event.FirstTokenTime > 0 && event.FirstTokenTime <= timeout
	if isAlgorithmSuccess {
		result.AlgorithmSuccess++
		channelResult.AlgorithmSuccess++
	} else {
		result.AlgorithmTimeout++
		channelResult.AlgorithmTimeout++
	}

	if isOracleSuccess && !isAlgorithmSuccess {
		result.FalsePositive++
		channelResult.FalsePositive++
	}
	if !isOracleSuccess && isAlgorithmSuccess {
		result.FalseNegative++
	}

	if isOracleSuccess {
		manager.RecordSuccess(event.ChannelID, event.KeyID, event.Model, event.FirstTokenTime)
	} else {
		manager.RecordTimeout(event.ChannelID, event.KeyID, event.Model, timeout)
	}
}

func finalizeResult(result *ReplayResult, minRequests int64) {
	if result.OracleSuccess > 0 {
		result.FalsePositiveRate = float64(result.FalsePositive) / float64(result.OracleSuccess)
	}
	if result.TotalEvents > 0 {
		result.TimeoutRate = float64(result.AlgorithmTimeout) / float64(result.TotalEvents)
	}
	for _, channelResult := range result.ChannelResults {
		if channelResult.TotalEvents >= minRequests {
			result.CoveredEvents += channelResult.TotalEvents
		}
		if channelResult.OracleSuccess > 0 {
			channelResult.FalsePositiveRate = float64(channelResult.FalsePositive) / float64(channelResult.OracleSuccess)
		}
	}
}

func parseLogFile(logFile, startTime, endTime string) ([]*LogEvent, error) {
	start, end, err := parseTimeRange(startTime, endTime)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(logFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	events := make([]*LogEvent, 0)
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event LogEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if event.ChannelID == 0 || event.Model == "" {
			return nil, fmt.Errorf("line %d: channel_id and model are required", lineNo)
		}
		if event.FirstTokenMS > 0 {
			event.FirstTokenTime = time.Duration(event.FirstTokenMS) * time.Millisecond
		}
		if !start.IsZero() && event.Timestamp.Before(start) {
			continue
		}
		if !end.IsZero() && event.Timestamp.After(end) {
			continue
		}
		events = append(events, &event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, errors.New("no replay events found")
	}
	return events, nil
}

func parseTimeRange(startTime, endTime string) (time.Time, time.Time, error) {
	var start, end time.Time
	var err error
	if startTime != "" {
		start, err = time.Parse(time.RFC3339, startTime)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start: %w", err)
		}
	}
	if endTime != "" {
		end, err = time.Parse(time.RFC3339, endTime)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end: %w", err)
		}
	}
	return start, end, nil
}

func saveReport(outputDir string, result *ReplayResult) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "replay-report.json"), data, 0644); err != nil {
		return err
	}

	markdown := buildMarkdownReport(result)
	return os.WriteFile(filepath.Join(outputDir, "replay-report.md"), []byte(markdown), 0644)
}

func buildMarkdownReport(result *ReplayResult) string {
	channelIDs := make([]int, 0, len(result.ChannelResults))
	for channelID := range result.ChannelResults {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)

	var b strings.Builder
	fmt.Fprintf(&b, "# Health Replay Report\n\n")
	fmt.Fprintf(&b, "- Total Events: %d\n", result.TotalEvents)
	fmt.Fprintf(&b, "- Covered Events: %d\n", result.CoveredEvents)
	fmt.Fprintf(&b, "- Oracle Success: %d\n", result.OracleSuccess)
	fmt.Fprintf(&b, "- Algorithm Timeout: %d\n", result.AlgorithmTimeout)
	fmt.Fprintf(&b, "- False Positive Rate: %.4f\n", result.FalsePositiveRate)
	fmt.Fprintf(&b, "- Result: %s\n\n", passText(passesAcceptance(result)))
	fmt.Fprintf(&b, "| Channel | Events | Oracle Success | Algorithm Timeout | False Positive | FP Rate |\n")
	fmt.Fprintf(&b, "|---:|---:|---:|---:|---:|---:|\n")
	for _, channelID := range channelIDs {
		item := result.ChannelResults[channelID]
		fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %.4f |\n", channelID, item.TotalEvents, item.OracleSuccess, item.AlgorithmTimeout, item.FalsePositive, item.FalsePositiveRate)
	}
	return b.String()
}

func passesAcceptance(result *ReplayResult) bool {
	return result.TotalEvents > 0 && result.FalsePositiveRate <= 0.02
}

func passText(ok bool) string {
	if ok {
		return "PASSED"
	}
	return "FAILED"
}

func getOrCreateChannelResult(result *ReplayResult, channelID int) *ChannelReplayResult {
	if _, ok := result.ChannelResults[channelID]; !ok {
		result.ChannelResults[channelID] = &ChannelReplayResult{ChannelID: channelID}
	}
	return result.ChannelResults[channelID]
}

func printResult(result *ReplayResult) {
	fmt.Printf("Replay events=%d covered=%d fp_rate=%.4f timeout_rate=%.4f\n", result.TotalEvents, result.CoveredEvents, result.FalsePositiveRate, result.TimeoutRate)
}
