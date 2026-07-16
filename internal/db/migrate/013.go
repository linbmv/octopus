package migrate

import (
	"fmt"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func init() {
	RegisterBeforeAutoMigration(Migration{Version: 13, Up: addStableEntityUUIDs})
}

func addStableEntityUUIDs(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	entities := []struct {
		name  string
		model any
	}{
		{name: "channels", model: &model.Channel{}},
		{name: "channel_keys", model: &model.ChannelKey{}},
		{name: "groups", model: &model.Group{}},
		{name: "group_items", model: &model.GroupItem{}},
		{name: "api_keys", model: &model.APIKey{}},
	}
	for _, entity := range entities {
		if !db.Migrator().HasTable(entity.model) {
			continue
		}
		if !db.Migrator().HasColumn(entity.model, "UUID") {
			if err := db.Migrator().AddColumn(entity.model, "UUID"); err != nil {
				return fmt.Errorf("add %s.uuid: %w", entity.name, err)
			}
		}
		if err := populateEntityUUIDs(db, entity.name); err != nil {
			return err
		}
		if !db.Migrator().HasIndex(entity.model, "UUID") {
			if err := db.Migrator().CreateIndex(entity.model, "UUID"); err != nil {
				return fmt.Errorf("create %s UUID index: %w", entity.name, err)
			}
		}
	}
	return nil
}

func populateEntityUUIDs(db *gorm.DB, table string) error {
	var rows []struct {
		ID   int
		UUID string
	}
	if err := db.Table(table).Select("id", "uuid").Order("id ASC").Scan(&rows).Error; err != nil {
		return fmt.Errorf("read %s UUIDs: %w", table, err)
	}
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		value := strings.TrimSpace(row.UUID)
		if value == "" {
			value = uuid.NewString()
			if err := db.Table(table).Where("id = ? AND (uuid IS NULL OR uuid = '')", row.ID).Update("uuid", value).Error; err != nil {
				return fmt.Errorf("populate %s row %d UUID: %w", table, row.ID, err)
			}
		} else {
			parsed, err := uuid.Parse(value)
			if err != nil {
				return fmt.Errorf("%s row %d has invalid UUID %q: %w", table, row.ID, value, err)
			}
			value = parsed.String()
			if value != row.UUID {
				if err := db.Table(table).Where("id = ?", row.ID).Update("uuid", value).Error; err != nil {
					return fmt.Errorf("normalize %s row %d UUID: %w", table, row.ID, err)
				}
			}
		}
		if previous, exists := seen[value]; exists {
			return fmt.Errorf("%s rows %d and %d share UUID %s", table, previous, row.ID, value)
		}
		seen[value] = row.ID
	}
	return nil
}
