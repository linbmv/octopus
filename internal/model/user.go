package model

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	UserAuthModeBearer = "bearer"
	UserAuthModeCookie = "cookie"
)

var ErrInvalidUserAuthMode = errors.New("invalid user authentication mode")

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Username string `gorm:"unique"`
	Password string `gorm:"not null"`
	// MustChangePassword 在首次以随机初始密码创建管理员时置为 true，
	// 强制其在改密前无法使用其他受保护接口。
	MustChangePassword bool `gorm:"not null;default:false"`
	// JWTSecret 是独立生成的高熵随机密钥，用于 JWT 签名，不再从 username+password 派生。
	// 确保备份/日志泄露中的用户数据不能用于伪造 token。
	// 空默认值仅用于兼容向已有数据的 SQLite 表添加 NOT NULL 列；AutoMigrate 后的
	// 升级迁移会为旧用户填入随机值，新用户也始终由 UserInit 显式生成随机值。
	JWTSecret string `gorm:"not null;default:''"`
	// TokenVersion 用于强制失效旧 token：改密/禁用/权限收回时递增版本号，
	// 验证 token 时同时检查版本，实现全局登出能力。
	TokenVersion int `gorm:"not null;default:0"`
}

type UserLogin struct {
	Username         string `json:"username" binding:"required,min=3,max=32"`
	Password         string `json:"password" binding:"required"`
	ExpiresInMinutes *int   `json:"expires_in_minutes,omitempty"`
	// LegacyExpire keeps the old request field working during the API transition.
	// Its historical backend unit is minutes even though an old frontend comment
	// incorrectly described it as seconds.
	LegacyExpire *int `json:"expire,omitempty"`
	// AuthMode defaults to bearer for compatibility with existing API clients.
	// The bundled browser UI explicitly requests cookie mode and never receives
	// or stores the resulting administrator credential.
	AuthMode string `json:"auth_mode,omitempty"`
}

type UserChangePassword struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

type UserChangeUsername struct {
	NewUsername     string `json:"new_username" binding:"required,min=3,max=32"`
	CurrentPassword string `json:"current_password" binding:"required"`
}

type UserLoginResponse struct {
	Token               string `json:"token,omitempty"`
	ExpireAt            string `json:"expire_at,omitempty"`
	MustChangePassword  bool   `json:"must_change_password"`
	AuthMode            string `json:"auth_mode"`
	WebAuthnRequired    bool   `json:"webauthn_required,omitempty"`
	WebAuthnTransaction string `json:"webauthn_transaction,omitempty"`
	WebAuthnOptions     any    `json:"webauthn_options,omitempty"`
}

func (u UserLogin) RequestedAuthMode() (string, error) {
	switch u.AuthMode {
	case "", UserAuthModeBearer:
		return UserAuthModeBearer, nil
	case UserAuthModeCookie:
		return UserAuthModeCookie, nil
	default:
		return "", fmt.Errorf("%w: must be bearer or cookie", ErrInvalidUserAuthMode)
	}
}

type UserStatusResponse struct {
	Username            string `json:"username"`
	MustChangePassword  bool   `json:"must_change_password"`
	WebAuthnEnabled     bool   `json:"webauthn_enabled"`
	WebAuthnCredentials int64  `json:"webauthn_credentials"`
}

type WebAuthnRegistrationBegin struct {
	Name            string `json:"name" binding:"required,min=1,max=100"`
	CurrentPassword string `json:"current_password" binding:"required"`
}

type WebAuthnCredentialDelete struct {
	ID              uint   `json:"id" binding:"required"`
	CurrentPassword string `json:"current_password" binding:"required"`
}

// RequestedExpiryMinutes resolves the explicit expiry field while accepting
// the legacy field for one compatibility window. Sending both fields is
// rejected because silently choosing one makes the security contract unclear.
func (u UserLogin) RequestedExpiryMinutes() (int, error) {
	if u.ExpiresInMinutes != nil && u.LegacyExpire != nil {
		return 0, fmt.Errorf("expires_in_minutes and expire cannot both be set")
	}
	if u.ExpiresInMinutes != nil {
		return *u.ExpiresInMinutes, nil
	}
	if u.LegacyExpire != nil {
		return *u.LegacyExpire, nil
	}
	return 0, nil
}

func (u *User) HashPassword() error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	u.Password = string(hashedPassword)
	return nil
}

func (u *User) ComparePassword(password string) error {
	return bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
}
