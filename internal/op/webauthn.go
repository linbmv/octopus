package op

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	wa "github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

var (
	ErrWebAuthnCredentialNotFound = errors.New("WebAuthn credential not found")
	ErrWebAuthnCredentialExists   = errors.New("WebAuthn credential already exists")
)

type WebAuthnUser struct {
	user        model.User
	credentials []wa.Credential
}

func (u *WebAuthnUser) WebAuthnID() []byte {
	// JWTSecret is random, stable for the account lifetime, and restored by
	// backups. Hashing prevents exposing it as the authenticator user handle.
	handle := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", u.user.ID, u.user.JWTSecret)))
	return handle[:]
}

func (u *WebAuthnUser) WebAuthnName() string { return u.user.Username }

func (u *WebAuthnUser) WebAuthnDisplayName() string { return u.user.Username }

func (u *WebAuthnUser) WebAuthnCredentials() []wa.Credential {
	return append([]wa.Credential(nil), u.credentials...)
}

func LoadWebAuthnUser(ctx context.Context) (*WebAuthnUser, error) {
	user := UserGet()
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]model.WebAuthnCredential, 0)
	if err := conn.Where("user_id = ?", user.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load WebAuthn credentials: %w", err)
	}
	credentials := make([]wa.Credential, 0, len(rows))
	for _, row := range rows {
		var credential wa.Credential
		if err := json.Unmarshal([]byte(row.CredentialJSON), &credential); err != nil {
			return nil, fmt.Errorf("decode WebAuthn credential %d: %w", row.ID, err)
		}
		credentials = append(credentials, credential)
	}
	return &WebAuthnUser{user: user, credentials: credentials}, nil
}

func WebAuthnCredentialCount(ctx context.Context) (int64, error) {
	user := UserGet()
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := conn.Model(&model.WebAuthnCredential{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count WebAuthn credentials: %w", err)
	}
	return count, nil
}

func WebAuthnCredentialList(ctx context.Context) ([]model.WebAuthnCredentialInfo, error) {
	user := UserGet()
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]model.WebAuthnCredential, 0)
	if err := conn.Where("user_id = ?", user.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list WebAuthn credentials: %w", err)
	}
	result := make([]model.WebAuthnCredentialInfo, 0, len(rows))
	for _, row := range rows {
		result = append(result, model.WebAuthnCredentialInfo{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return result, nil
}

func WebAuthnCredentialCreate(ctx context.Context, name string, credential *wa.Credential) error {
	if credential == nil || len(credential.ID) == 0 {
		return fmt.Errorf("credential is empty")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return fmt.Errorf("credential name must contain between 1 and 100 bytes")
	}
	payload, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode WebAuthn credential: %w", err)
	}
	user := UserGet()
	row := model.WebAuthnCredential{
		UserID:         user.ID,
		Name:           name,
		CredentialID:   base64.RawURLEncoding.EncodeToString(credential.ID),
		CredentialJSON: string(payload),
	}
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return err
	}
	if err := conn.Create(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrWebAuthnCredentialExists
		}
		return fmt.Errorf("create WebAuthn credential: %w", err)
	}
	return nil
}

func WebAuthnCredentialUpdate(ctx context.Context, credential *wa.Credential) error {
	if credential == nil || len(credential.ID) == 0 {
		return fmt.Errorf("credential is empty")
	}
	payload, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode WebAuthn credential: %w", err)
	}
	user := UserGet()
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return err
	}
	credentialID := base64.RawURLEncoding.EncodeToString(credential.ID)
	result := conn.Model(&model.WebAuthnCredential{}).
		Where("user_id = ? AND credential_id_hash = ? AND credential_id = ?", user.ID, model.WebAuthnCredentialIDHash(credentialID), credentialID).
		Update("credential_json", string(payload))
	if result.Error != nil {
		return fmt.Errorf("update WebAuthn credential: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrWebAuthnCredentialNotFound
	}
	return nil
}

func WebAuthnCredentialDelete(ctx context.Context, id uint) error {
	user := UserGet()
	conn, err := webAuthnDB(ctx)
	if err != nil {
		return err
	}
	result := conn.Where("id = ? AND user_id = ?", id, user.ID).Delete(&model.WebAuthnCredential{})
	if result.Error != nil {
		return fmt.Errorf("delete WebAuthn credential: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrWebAuthnCredentialNotFound
	}
	return nil
}

func webAuthnDB(ctx context.Context) (*gorm.DB, error) {
	conn := db.GetDB()
	if conn == nil {
		return nil, fmt.Errorf("WebAuthn database is not initialized")
	}
	return conn.WithContext(ctx), nil
}
