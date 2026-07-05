package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 8,
		Up:      addRelayLogTimeIndex,
	})
}

func addRelayLogTimeIndex(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	if db.Migrator().HasIndex(&model.RelayLog{}, "idx_relay_logs_time") {
		return nil
	}
	if err := db.Migrator().CreateIndex(&model.RelayLog{}, "idx_relay_logs_time"); err != nil {
		return fmt.Errorf("failed to create relay_logs.time index: %w", err)
	}
	return nil
}
