package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func init() {
	RegisterAfterAutoMigration(Migration{Version: 16, Up: backfillChannelKeyStats})
}

// backfillChannelKeyStats introduces credential-scoped counters without
// changing the historical channel totals. Existing relay logs retain the
// per-attempt channel_key_id, so successful attempts and non-client failures
// can be reconstructed exactly using the same semantics as relay.go. Deleted
// credentials are intentionally ignored because their IDs are no longer
// present in channel_keys and must not reappear in the credential list.
func backfillChannelKeyStats(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable(&model.StatsChannelKey{}) {
		return fmt.Errorf("stats channel key stats table is missing")
	}

	var keys []model.ChannelKey
	if err := db.Select("id", "channel_id").Find(&keys).Error; err != nil {
		return fmt.Errorf("read channel keys for stats backfill: %w", err)
	}
	owners := make(map[int]int, len(keys))
	for _, key := range keys {
		if key.ID > 0 && key.ChannelID > 0 {
			owners[key.ID] = key.ChannelID
		}
	}

	aggregates := make(map[int]model.StatsChannelKey)
	rows, err := db.Model(&model.RelayLog{}).Select("id", "attempts").Rows()
	if err != nil {
		return fmt.Errorf("read relay logs for channel key stats backfill: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var relayLog model.RelayLog
		if err := db.ScanRows(rows, &relayLog); err != nil {
			return fmt.Errorf("scan relay log for channel key stats backfill: %w", err)
		}
		for _, attempt := range relayLog.Attempts {
			if attempt.ChannelKeyID <= 0 || attempt.ChannelID <= 0 {
				continue
			}
			if owner, ok := owners[attempt.ChannelKeyID]; !ok || owner != attempt.ChannelID {
				continue
			}

			stats := aggregates[attempt.ChannelKeyID]
			stats.ChannelID = attempt.ChannelID
			stats.ChannelKeyID = attempt.ChannelKeyID
			switch {
			case attempt.Status == model.AttemptSuccess:
				stats.RequestSuccess++
				stats.WaitTime += int64(attempt.Duration)
			case attempt.Status == model.AttemptFailed && attempt.ErrorLevel != model.AttemptErrorLevelClient:
				stats.RequestFailed++
				stats.WaitTime += int64(attempt.Duration)
			}
			aggregates[attempt.ChannelKeyID] = stats
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate relay logs for channel key stats backfill: %w", err)
	}

	if len(aggregates) == 0 {
		return nil
	}
	rowsToWrite := make([]model.StatsChannelKey, 0, len(aggregates))
	for _, stats := range aggregates {
		rowsToWrite = append(rowsToWrite, stats)
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_key_id"}},
		UpdateAll: true,
	}).Create(&rowsToWrite).Error; err != nil {
		return fmt.Errorf("write channel key stats backfill: %w", err)
	}
	return nil
}
