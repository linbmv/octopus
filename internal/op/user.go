package op

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm"
)

var userService = NewUserService()

type UserService struct {
	mu   sync.RWMutex
	user model.User
}

func NewUserService() *UserService {
	return &UserService{}
}

func UserInit() error {
	return userService.Init(context.Background())
}

func (s *UserService) Init(ctx context.Context) error {
	var user model.User
	err := db.GetDB().WithContext(ctx).First(&user).Error
	if err == nil {
		s.mu.Lock()
		s.user = user
		s.mu.Unlock()
		return nil
	}
	// 仅在确实没有用户时创建初始管理员；其他数据库错误（锁、连接、schema）
	// 必须向上返回，不能当成“用户不存在”而误建 admin。
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query initial user: %w", err)
	}

	initialPassword, err := generateInitialPassword()
	if err != nil {
		return fmt.Errorf("generate initial password: %w", err)
	}
	jwtSecret, err := generateJWTSecret()
	if err != nil {
		return fmt.Errorf("generate jwt secret: %w", err)
	}
	user = model.User{
		Username:           "admin",
		Password:           initialPassword,
		MustChangePassword: true,
		JWTSecret:          jwtSecret,
		TokenVersion:       0,
	}
	if err := user.HashPassword(); err != nil {
		return err
	}
	if err := db.GetDB().WithContext(ctx).Create(&user).Error; err != nil {
		return err
	}
	s.mu.Lock()
	s.user = user
	s.mu.Unlock()
	// 随机初始密码仅在此打印一次；用户须在首次登录后立即改密。
	log.Warnf("initial admin created — username: admin, password: %s (change it immediately; shown only once)", initialPassword)
	return nil
}

// generateInitialPassword 生成 24 字符的高熵随机密码，用于初始管理员。
func generateInitialPassword() (string, error) {
	const passwordChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const length = 24
	b := make([]byte, length)
	maxI := big.NewInt(int64(len(passwordChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return "", err
		}
		b[i] = passwordChars[n.Int64()]
	}
	return string(b), nil
}

// generateJWTSecret 生成 32 字节（256 位）的高熵随机 JWT 密钥。
func generateJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	// 使用 base64 编码存储，避免二进制数据存储问题
	return fmt.Sprintf("%x", b), nil
}

func UserChangePassword(oldPassword, newPassword string) error {
	return userService.ChangePassword(context.Background(), oldPassword, newPassword)
}

func (s *UserService) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.user.ComparePassword(oldPassword); err != nil {
		return fmt.Errorf("incorrect old password: %w", err)
	}

	next := s.user
	next.Password = newPassword
	if err := next.HashPassword(); err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	// 改密同时：清除强制改密标记 + 递增 token_version 使旧 token 失效。
	newVersion := s.user.TokenVersion + 1
	if err := db.GetDB().WithContext(ctx).Model(&s.user).
		Updates(map[string]any{
			"password":             next.Password,
			"must_change_password": false,
			"token_version":        newVersion,
		}).Error; err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	s.user.Password = next.Password
	s.user.MustChangePassword = false
	s.user.TokenVersion = newVersion
	return nil
}

func UserChangeUsername(newUsername string) error {
	return userService.ChangeUsername(context.Background(), newUsername)
}

func (s *UserService) ChangeUsername(ctx context.Context, newUsername string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.user.Username == newUsername {
		return fmt.Errorf("new username is the same as the old username")
	}
	if err := db.GetDB().WithContext(ctx).Model(&s.user).Update("username", newUsername).Error; err != nil {
		return fmt.Errorf("failed to update username: %w", err)
	}
	s.user.Username = newUsername
	return nil
}

func UserVerify(username, password string) error {
	return userService.Verify(username, password)
}

func (s *UserService) Verify(username, password string) error {
	s.mu.RLock()
	user := s.user
	s.mu.RUnlock()

	if username != user.Username {
		return fmt.Errorf("incorrect username")
	}
	if err := user.ComparePassword(password); err != nil {
		return fmt.Errorf("incorrect password")
	}
	return nil
}

func UserGet() model.User {
	return userService.Get()
}

func (s *UserService) Get() model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user
}

// UserMustChangePassword 返回当前用户是否处于强制改密状态。
func UserMustChangePassword() bool {
	userService.mu.RLock()
	defer userService.mu.RUnlock()
	return userService.user.MustChangePassword
}
