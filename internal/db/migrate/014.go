package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{Version: 14, Up: backfillWebAuthnCredentialHashes})
}

func backfillWebAuthnCredentialHashes(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable(&model.WebAuthnCredential{}) ||
		!db.Migrator().HasColumn(&model.WebAuthnCredential{}, "CredentialHash") {
		return nil
	}
	var rows []model.WebAuthnCredential
	if err := db.Select("id", "credential_id", "credential_id_hash").Order("id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("read WebAuthn credential IDs: %w", err)
	}
	type hashUpdate struct {
		id   uint
		hash string
	}
	updates := make([]hashUpdate, 0, len(rows))
	seen := make(map[string]uint, len(rows))
	for _, row := range rows {
		if row.CredentialID == "" {
			return fmt.Errorf("WebAuthn credential %d has an empty credential ID", row.ID)
		}
		hash := model.WebAuthnCredentialIDHash(row.CredentialID)
		if previous, exists := seen[hash]; exists {
			return fmt.Errorf("WebAuthn credentials %d and %d have duplicate credential IDs", previous, row.ID)
		}
		seen[hash] = row.ID
		if row.CredentialHash != hash {
			updates = append(updates, hashUpdate{id: row.ID, hash: hash})
		}
	}
	for _, update := range updates {
		if err := db.Model(&model.WebAuthnCredential{}).Where("id = ?", update.id).
			UpdateColumn("credential_id_hash", update.hash).Error; err != nil {
			return fmt.Errorf("backfill WebAuthn credential %d hash: %w", update.id, err)
		}
	}
	return nil
}
