package op

import (
	"context"
	"fmt"
	"sync"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
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
	if err := db.GetDB().WithContext(ctx).First(&user).Error; err == nil {
		s.mu.Lock()
		s.user = user
		s.mu.Unlock()
		return nil
	}
	user.Username = "admin"
	user.Password = "admin"
	if err := user.HashPassword(); err != nil {
		return err
	}
	if err := db.GetDB().WithContext(ctx).Create(&user).Error; err != nil {
		return err
	}
	s.mu.Lock()
	s.user = user
	s.mu.Unlock()
	log.Infof("initial user: admin,password: admin")
	return nil
}

func UserChangePassword(oldPassword, newPassword string) error {
	return userService.ChangePassword(context.Background(), oldPassword, newPassword)
}

func UserChangePasswordContext(ctx context.Context, oldPassword, newPassword string) error {
	return userService.ChangePassword(ctx, oldPassword, newPassword)
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

	if err := db.GetDB().WithContext(ctx).Model(&s.user).Update("password", next.Password).Error; err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	s.user.Password = next.Password
	return nil
}

func UserChangeUsername(newUsername string) error {
	return userService.ChangeUsername(context.Background(), newUsername)
}

func UserChangeUsernameContext(ctx context.Context, newUsername string) error {
	return userService.ChangeUsername(ctx, newUsername)
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
