package model

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Username string `gorm:"unique"`
	Password string `gorm:"not null"`
	// MustChangePassword 在首次以随机初始密码创建管理员时置为 true，
	// 强制其在改密前无法使用其他受保护接口。
	MustChangePassword bool   `gorm:"not null;default:false"`
	// JWTSecret 是独立生成的高熵随机密钥，用于 JWT 签名，不再从 username+password 派生。
	// 确保备份/日志泄露中的用户数据不能用于伪造 token。
	JWTSecret string `gorm:"not null"`
	// TokenVersion 用于强制失效旧 token：改密/禁用/权限收回时递增版本号，
	// 验证 token 时同时检查版本，实现全局登出能力。
	TokenVersion int `gorm:"not null;default:0"`
}

type UserLogin struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required"`
	Expire   int    `json:"expire"`
}

type UserChangePassword struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8"`
}

type UserChangeUsername struct {
	NewUsername     string `json:"new_username" binding:"required,min=3,max=32"`
	CurrentPassword string `json:"current_password" binding:"required"`
}

type UserLoginResponse struct {
	Token              string `json:"token"`
	ExpireAt           string `json:"expire_at"`
	MustChangePassword bool   `json:"must_change_password"`
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
