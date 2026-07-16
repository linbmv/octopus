package op

import (
	"context"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestRelayLogErrorLevelsAggregatesClassifiedFailuresByChannelAndHour(t *testing.T) {
	initTestDB(t)
	now := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
	service := NewRelayLogService()
	logs := []model.RelayLog{
		{
			ID: 1, Time: now.Add(-2 * time.Hour).Unix(), RequestContent: "must-not-be-selected", ResponseContent: "must-not-be-selected",
			Attempts: []model.ChannelAttempt{
				{ChannelID: 1, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelKey, ErrorReason: "rate limited"},
				{ChannelID: 2, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelChannel, ErrorReason: "bad gateway"},
			},
		},
		{
			ID: 2, Time: now.Add(-time.Hour).Unix(),
			Attempts: []model.ChannelAttempt{
				{ChannelID: 1, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelClient, ErrorReason: "invalid request"},
				// Defensive aggregation boundary: success/error_level and legacy
				// failed attempts without a level must not pollute the distribution.
				{ChannelID: 1, Status: model.AttemptSuccess, ErrorLevel: model.AttemptErrorLevelKey},
				{ChannelID: 1, Status: model.AttemptFailed},
			},
		},
		{ID: 3, Time: now.Add(-25 * time.Hour).Unix(), Attempts: []model.ChannelAttempt{{ChannelID: 1, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelKey}}},
	}
	if err := db.GetDB().Create(&logs).Error; err != nil {
		t.Fatalf("create relay logs: %v", err)
	}
	// A not-yet-flushed cache entry is included, while a cache/DB duplicate is
	// deduplicated by Snowflake ID.
	service.cache = []model.RelayLog{
		logs[1],
		{ID: 4, Time: now.Add(-15 * time.Minute).Unix(), Attempts: []model.ChannelAttempt{{ChannelID: 1, Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelChannel, ErrorReason: "timeout"}}},
	}

	global, err := service.ErrorLevels(context.Background(), 24, 0, now)
	if err != nil {
		t.Fatalf("ErrorLevels(global): %v", err)
	}
	if global.Counts != (model.StatsErrorLevelCounts{Key: 1, Channel: 2, Client: 1}) {
		t.Fatalf("global counts = %+v", global.Counts)
	}
	if global.ScannedLogs != 3 || global.Truncated || global.Capacity != StatsErrorLevelsScanCapacity {
		t.Fatalf("global scan metadata = %+v", global)
	}
	source, _, err := service.errorLevelSourceLogs(context.Background(), now.Add(-24*time.Hour).Unix(), now.Unix(), 10)
	if err != nil {
		t.Fatalf("errorLevelSourceLogs: %v", err)
	}
	for _, entry := range source {
		if entry.ID == 1 && (entry.RequestContent != "" || entry.ResponseContent != "") {
			t.Fatal("error statistics query selected persisted request/response bodies")
		}
	}
	if len(global.Trend) != 3 {
		t.Fatalf("global trend points = %d, want 3: %+v", len(global.Trend), global.Trend)
	}
	for i := 1; i < len(global.Trend); i++ {
		if global.Trend[i-1].BucketStart >= global.Trend[i].BucketStart {
			t.Fatalf("trend is not ascending: %+v", global.Trend)
		}
	}

	channel, err := service.ErrorLevels(context.Background(), 24, 1, now)
	if err != nil {
		t.Fatalf("ErrorLevels(channel): %v", err)
	}
	if channel.Counts != (model.StatsErrorLevelCounts{Key: 1, Channel: 1, Client: 1}) {
		t.Fatalf("channel counts = %+v", channel.Counts)
	}
	if channel.ChannelID != 1 {
		t.Fatalf("channel id = %d, want 1", channel.ChannelID)
	}
}

func TestRelayLogErrorLevelsCapacityUsesNewestLogsAndReportsTruncation(t *testing.T) {
	initTestDB(t)
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	service := NewRelayLogService()
	logs := []model.RelayLog{
		{ID: 1, Time: now.Add(-3 * time.Hour).Unix(), Attempts: []model.ChannelAttempt{{Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelClient}}},
		{ID: 2, Time: now.Add(-2 * time.Hour).Unix(), Attempts: []model.ChannelAttempt{{Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelKey}}},
		{ID: 3, Time: now.Add(-time.Hour).Unix(), Attempts: []model.ChannelAttempt{{Status: model.AttemptFailed, ErrorLevel: model.AttemptErrorLevelChannel}}},
	}
	if err := db.GetDB().Create(&logs).Error; err != nil {
		t.Fatalf("create relay logs: %v", err)
	}

	got, err := service.errorLevelsWithCapacity(context.Background(), 24, 0, now, 2)
	if err != nil {
		t.Fatalf("errorLevelsWithCapacity: %v", err)
	}
	if !got.Truncated || got.ScannedLogs != 2 || got.Capacity != 2 {
		t.Fatalf("scan metadata = %+v", got)
	}
	if got.Counts != (model.StatsErrorLevelCounts{Key: 1, Channel: 1}) {
		t.Fatalf("newest bounded counts = %+v", got.Counts)
	}
}

func TestRelayLogErrorLevelsRejectsUnboundedInputs(t *testing.T) {
	service := NewRelayLogService()
	for _, test := range []struct {
		window    int
		channelID int
		capacity  int
	}{
		{window: 0, capacity: 1},
		{window: StatsErrorLevelsMaxWindowHours + 1, capacity: 1},
		{window: 1, channelID: -1, capacity: 1},
		{window: 1, capacity: 0},
		{window: 1, capacity: StatsErrorLevelsScanCapacity + 1},
	} {
		if _, err := service.errorLevelsWithCapacity(context.Background(), test.window, test.channelID, time.Now(), test.capacity); err == nil {
			t.Fatalf("input %+v unexpectedly succeeded", test)
		}
	}
}
