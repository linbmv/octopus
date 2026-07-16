package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// WebAuthnCredential stores the complete credential record emitted by the
// WebAuthn library. CredentialJSON must round-trip without dropping fields
// because signature counters and authenticator flags are security state.
type WebAuthnCredential struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"not null;index" json:"user_id"`
	Name           string    `gorm:"not null;size:100" json:"name"`
	CredentialID   string    `gorm:"not null;size:1366" json:"credential_id"`
	CredentialHash string    `gorm:"column:credential_id_hash;size:64;uniqueIndex" json:"-"`
	CredentialJSON string    `gorm:"not null;type:text" json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func WebAuthnCredentialIDHash(credentialID string) string {
	sum := sha256.Sum256([]byte(credentialID))
	return hex.EncodeToString(sum[:])
}

func (c *WebAuthnCredential) BeforeCreate(_ *gorm.DB) error {
	if c == nil || c.CredentialID == "" {
		return fmt.Errorf("WebAuthn credential ID is required")
	}
	c.CredentialHash = WebAuthnCredentialIDHash(c.CredentialID)
	return nil
}

type WebAuthnCredentialInfo struct {
	ID        uint      `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
