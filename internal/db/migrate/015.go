package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{Version: 15, Up: ensureReasoningTokenColumns})
}

func ensureReasoningTokenColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	columns := []struct {
		name  string
		model any
		field string
	}{
		{name: "stats_totals.reasoning_token", model: &model.StatsTotal{}, field: "ReasoningToken"},
		{name: "stats_dailies.reasoning_token", model: &model.StatsDaily{}, field: "ReasoningToken"},
		{name: "stats_hourlies.reasoning_token", model: &model.StatsHourly{}, field: "ReasoningToken"},
		{name: "stats_channels.reasoning_token", model: &model.StatsChannel{}, field: "ReasoningToken"},
		{name: "stats_api_keys.reasoning_token", model: &model.StatsAPIKey{}, field: "ReasoningToken"},
		{name: "relay_logs.reasoning_tokens", model: &model.RelayLog{}, field: "ReasoningTokens"},
	}
	for _, column := range columns {
		if !db.Migrator().HasTable(column.model) {
			return fmt.Errorf("reasoning token table for %s is missing", column.name)
		}
		if !db.Migrator().HasColumn(column.model, column.field) {
			return fmt.Errorf("reasoning token column %s was not created by AutoMigrate", column.name)
		}
	}
	return nil
}
