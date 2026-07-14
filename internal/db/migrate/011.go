package migrate

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"gorm.io/gorm"
)

func init() {
	// User.JWTSecret 带有仅用于 schema 升级的空默认值，因此 AutoMigrate 可以先在
	// 已有数据的 SQLite 表上安全加列；随后本迁移把旧用户的空值替换为随机密钥。
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
	// 全新安装尚无 users 表，由紧随其后的 AutoMigrate 一次性创建完整表结构；
	// 此处只负责升级已经存在的旧表。
	if !db.Migrator().HasTable("users") {
		return nil
	}

	// 检查字段是否已存在
	hasJWTSecret := db.Migrator().HasColumn("users", "jwt_secret")
	hasTokenVersion := db.Migrator().HasColumn("users", "token_version")

	// 正常启动时 AutoMigrate 已经加列；以下分支保留为直接调用迁移时的兼容兜底。
	if !hasJWTSecret {
		// VARCHAR(128) 足以容纳当前 32 字节随机值的 base64/hex 编码，并且带默认值的
		// NOT NULL 列可安全添加到已有数据的 SQLite/MySQL/PostgreSQL 表。
		if err := db.Exec("ALTER TABLE users ADD COLUMN jwt_secret VARCHAR(128) NOT NULL DEFAULT ''").Error; err != nil {
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
		ID        uint   `gorm:"column:id"`
		JWTSecret string `gorm:"column:jwt_secret"`
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
