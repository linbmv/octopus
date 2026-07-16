package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBackfillWebAuthnCredentialHashes(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.Exec(`CREATE TABLE web_authn_credentials (
		id integer PRIMARY KEY, user_id integer, name text, credential_id text,
		credential_json text, created_at datetime, updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy credential table: %v", err)
	}
	if err := database.Exec(`INSERT INTO web_authn_credentials
		(id, user_id, name, credential_id, credential_json) VALUES
		(1, 1, 'key', 'credential-value', '{}')`).Error; err != nil {
		t.Fatalf("insert legacy credential: %v", err)
	}
	if err := database.AutoMigrate(&model.WebAuthnCredential{}); err != nil {
		t.Fatalf("auto migrate credential hash: %v", err)
	}
	if err := backfillWebAuthnCredentialHashes(database); err != nil {
		t.Fatalf("backfill credential hash: %v", err)
	}
	var row model.WebAuthnCredential
	if err := database.First(&row, 1).Error; err != nil {
		t.Fatalf("load migrated credential: %v", err)
	}
	if want := model.WebAuthnCredentialIDHash(row.CredentialID); row.CredentialHash != want {
		t.Fatalf("credential hash = %q, want %q", row.CredentialHash, want)
	}
}

func TestBackfillWebAuthnCredentialHashesRejectsDuplicatesBeforeWriting(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&model.WebAuthnCredential{}); err != nil {
		t.Fatalf("migrate credential table: %v", err)
	}
	for _, row := range []model.WebAuthnCredential{
		{ID: 1, UserID: 1, Name: "one", CredentialID: "duplicate", CredentialJSON: "{}"},
		{ID: 2, UserID: 1, Name: "two", CredentialID: "duplicate", CredentialJSON: "{}"},
	} {
		if err := database.Session(&gorm.Session{SkipHooks: true}).Omit("credential_id_hash").Create(&row).Error; err != nil {
			t.Fatalf("insert legacy duplicate: %v", err)
		}
	}
	if err := backfillWebAuthnCredentialHashes(database); err == nil {
		t.Fatal("duplicate credential IDs were accepted")
	}
	var populated int64
	if err := database.Model(&model.WebAuthnCredential{}).Where("credential_id_hash IS NOT NULL AND credential_id_hash <> ''").Count(&populated).Error; err != nil {
		t.Fatalf("count populated hashes: %v", err)
	}
	if populated != 0 {
		t.Fatalf("duplicate validation partially wrote %d hashes", populated)
	}
}
