package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestReasoningTokenColumnsUpgradePopulatedLegacySQLite(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	legacyDDL := []string{
		`CREATE TABLE stats_totals (id integer PRIMARY KEY, input_token bigint, output_token bigint)`,
		`INSERT INTO stats_totals (id, input_token, output_token) VALUES (1, 12, 8)`,
		`CREATE TABLE relay_logs (id integer PRIMARY KEY, time bigint, input_tokens integer, output_tokens integer)`,
		`INSERT INTO relay_logs (id, time, input_tokens, output_tokens) VALUES (50, 1, 12, 8)`,
	}
	for _, statement := range legacyDDL {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("prepare legacy database with %q: %v", statement, err)
		}
	}

	if err := database.AutoMigrate(
		&model.StatsTotal{},
		&model.StatsDaily{},
		&model.StatsHourly{},
		&model.StatsChannel{},
		&model.StatsAPIKey{},
		&model.RelayLog{},
	); err != nil {
		t.Fatalf("auto migrate legacy database: %v", err)
	}
	if err := ensureReasoningTokenColumns(database); err != nil {
		t.Fatalf("verify reasoning token columns: %v", err)
	}

	var stats model.StatsTotal
	if err := database.First(&stats, 1).Error; err != nil {
		t.Fatalf("load migrated total stats: %v", err)
	}
	if stats.InputToken != 12 || stats.OutputToken != 8 || stats.ReasoningToken != 0 {
		t.Fatalf("migrated total stats = %#v, want original totals and zero reasoning", stats)
	}
	var relayLog model.RelayLog
	if err := database.First(&relayLog, 50).Error; err != nil {
		t.Fatalf("load migrated relay log: %v", err)
	}
	if relayLog.InputTokens != 12 || relayLog.OutputTokens != 8 || relayLog.ReasoningTokens != 0 {
		t.Fatalf("migrated relay log = %#v, want original totals and zero reasoning", relayLog)
	}

	if err := database.Model(&stats).Update("reasoning_token", 5).Error; err != nil {
		t.Fatalf("write total reasoning tokens: %v", err)
	}
	if err := database.Model(&relayLog).Update("reasoning_tokens", 5).Error; err != nil {
		t.Fatalf("write log reasoning tokens: %v", err)
	}
	if err := database.First(&stats, 1).Error; err != nil || stats.ReasoningToken != 5 {
		t.Fatalf("round-trip total reasoning tokens = %d, err=%v", stats.ReasoningToken, err)
	}
	if err := database.First(&relayLog, 50).Error; err != nil || relayLog.ReasoningTokens != 5 {
		t.Fatalf("round-trip log reasoning tokens = %d, err=%v", relayLog.ReasoningTokens, err)
	}
}
