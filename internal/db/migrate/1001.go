package migrate

import (
	"encoding/json"
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

const channelCompatibilityMigrationVersion = 1001

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: channelCompatibilityMigrationVersion,
		Up:      migrateChannelCompatibilityFields,
	})
}

// migrateChannelCompatibilityFields initializes the additive multi-endpoint
// representation from the current upstream single URL/key fields. It never
// removes or overwrites an existing multi-value entry, so it is safe to rerun
// and provides the storage seam required by the later Edge import adapter.
func migrateChannelCompatibilityFields(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("channels") || !db.Migrator().HasTable("channel_keys") {
		return nil
	}

	var channels []model.Channel
	if err := db.Find(&channels).Error; err != nil {
		return fmt.Errorf("load channels for compatibility migration: %w", err)
	}
	for _, channel := range channels {
		if len(channel.BaseUrls) == 0 && channel.BaseURL != "" {
			channel.BaseUrls = []model.BaseUrl{{URL: channel.BaseURL}}
			encoded, err := json.Marshal(channel.BaseUrls)
			if err != nil {
				return fmt.Errorf("encode channel %d base_urls: %w", channel.ID, err)
			}
			if err := db.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("base_urls", string(encoded)).Error; err != nil {
				return fmt.Errorf("backfill channel %d base_urls: %w", channel.ID, err)
			}
		}

		var keys []model.ChannelKey
		if err := db.Where("channel_id = ?", channel.ID).Order("id ASC").Find(&keys).Error; err != nil {
			return fmt.Errorf("load channel %d keys: %w", channel.ID, err)
		}
		if len(keys) == 0 && channel.Key != "" {
			key := model.ChannelKey{ChannelID: channel.ID, Enabled: true, ChannelKey: channel.Key}
			if err := db.Create(&key).Error; err != nil {
				return fmt.Errorf("backfill channel %d key: %w", channel.ID, err)
			}
		}
	}
	return nil
}
