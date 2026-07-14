package migrate

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 11,
		Up:      addJWTSecretAndTokenVersion,
	})
}

// addJWTSecretAndTokenVersion 为 users 表添加 jwt_secret 和 token_version 字段，
// 并为已存在用户生成高熵随机 JWT 密钥，确保 token 签名不再依赖可导出的用户数据。
func addJWTSecretAndTokenVersion(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	// 检查字段是否已存在
	hasJWTSecret := db.Migrator().HasColumn("users", "jwt_secret")
	hasTokenVersion := db.Migrator().HasColumn("users", "token_version")

	if hasJWTSecret && hasTokenVersion {
		return nil // 已迁移
	}

	// 添加字段
	if !hasJWTSecret {
		if err := db.Exec("ALTER TABLE users ADD COLUMN jwt_secret TEXT NOT NULL DEFAULT ''").Error; err != nil {
			return fmt.Errorf("failed to add jwt_secret column: %w", err)
		}
	}
	if !hasTokenVersion {
		if err := db.Exec("ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0").Error; err != nil {
			return fmt.Errorf("failed to add token_version column: %w", err)
		}
	}

	// 为已存在的用户生成 JWT 密钥
	type userRow struct {
		ID        uint
		JWTSecret string
	}
	var users []userRow
	if err := db.Raw("SELECT id, jwt_secret FROM users WHERE jwt_secret = '' OR jwt_secret IS NULL").Scan(&users).Error; err != nil {
		return fmt.Errorf("failed to query users: %w", err)
	}

	for _, u := range users {
		secret, err := generateJWTSecret()
		if err != nil {
			return fmt.Errorf("failed to generate jwt secret for user %d: %w", u.ID, err)
		}
		if err := db.Exec("UPDATE users SET jwt_secret = ? WHERE id = ?", secret, u.ID).Error; err != nil {
			return fmt.Errorf("failed to update jwt_secret for user %d: %w", u.ID, err)
		}
	}

	return nil
}

// generateJWTSecret 生成 32 字节（256 位）的高熵随机密钥，base64 编码后存储。
func generateJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
