package op

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

func TestUserServiceVerifyAndGet(t *testing.T) {
	user := model.User{Username: "admin", Password: "secret"}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	service := NewUserService()
	service.user = user

	if err := service.Verify("admin", "secret"); err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if err := service.Verify("admin", "wrong"); err == nil {
		t.Fatal("Verify accepted wrong password")
	}
	if err := service.Verify("other", "secret"); err == nil {
		t.Fatal("Verify accepted wrong username")
	}

	got := service.Get()
	got.Username = "mutated"
	if service.Get().Username != "admin" {
		t.Fatal("Get should return a copy of cached user")
	}
}

func TestValidateNewPasswordUsesUTF8ByteLength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "seven ASCII bytes", password: "1234567", wantErr: true},
		{name: "eight ASCII bytes", password: "12345678"},
		{name: "seventy two UTF-8 bytes", password: strings.Repeat("界", 24)},
		{name: "more than seventy two UTF-8 bytes", password: strings.Repeat("界", 25), wantErr: true},
		{name: "seventy three ASCII bytes", password: strings.Repeat("a", 73), wantErr: true},
		{name: "invalid UTF-8", password: "1234567\xff", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateNewPassword(test.password)
			if test.wantErr && !errors.Is(err, ErrInvalidPassword) {
				t.Fatalf("ValidateNewPassword() error = %v, want ErrInvalidPassword", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ValidateNewPassword() error = %v", err)
			}
		})
	}
}

func TestChangePasswordRejectsUnchangedPasswordWithoutMutatingState(t *testing.T) {
	service := newPersistedUserService(t, "initial-password", true, 7)

	err := service.ChangePassword(context.Background(), "initial-password", "initial-password")
	if !errors.Is(err, ErrPasswordUnchanged) {
		t.Fatalf("ChangePassword() error = %v, want ErrPasswordUnchanged", err)
	}
	got := service.Get()
	if !got.MustChangePassword || got.TokenVersion != 7 {
		t.Fatalf("cached state changed after rejection: %#v", got)
	}
	if err := got.ComparePassword("initial-password"); err != nil {
		t.Fatalf("stored password changed after rejection: %v", err)
	}

	var stored model.User
	if err := db.GetDB().First(&stored, got.ID).Error; err != nil {
		t.Fatalf("load persisted user: %v", err)
	}
	if !stored.MustChangePassword || stored.TokenVersion != 7 {
		t.Fatalf("persisted state changed after rejection: %#v", stored)
	}
}

func TestChangePasswordRejectsInvalidLengthBeforeMutation(t *testing.T) {
	service := newPersistedUserService(t, "initial-password", true, 3)
	for _, password := range []string{"short", strings.Repeat("a", 73)} {
		err := service.ChangePassword(context.Background(), "initial-password", password)
		if !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("ChangePassword(%d bytes) error = %v, want ErrInvalidPassword", len(password), err)
		}
	}
	got := service.Get()
	if !got.MustChangePassword || got.TokenVersion != 3 {
		t.Fatalf("cached state changed after invalid password: %#v", got)
	}
}

func TestChangeUsernameIncrementsTokenVersion(t *testing.T) {
	service := newPersistedUserService(t, "initial-password", false, 11)
	if err := service.ChangeUsername(context.Background(), "operator"); err != nil {
		t.Fatalf("ChangeUsername() error = %v", err)
	}
	got := service.Get()
	if got.Username != "operator" || got.TokenVersion != 12 {
		t.Fatalf("cached user = %#v, want operator/version 12", got)
	}

	var stored model.User
	if err := db.GetDB().First(&stored, got.ID).Error; err != nil {
		t.Fatalf("load persisted user: %v", err)
	}
	if stored.Username != "operator" || stored.TokenVersion != 12 {
		t.Fatalf("persisted user = %#v, want operator/version 12", stored)
	}
	if err := service.ChangeUsername(context.Background(), "operator"); !errors.Is(err, ErrUsernameUnchanged) {
		t.Fatalf("same username error = %v, want ErrUsernameUnchanged", err)
	}
	if service.Get().TokenVersion != 12 {
		t.Fatalf("same username changed token version to %d", service.Get().TokenVersion)
	}
}

func newPersistedUserService(t *testing.T, password string, mustChange bool, tokenVersion int) *UserService {
	t.Helper()
	initTestDB(t)
	user := model.User{
		Username:           "admin",
		Password:           password,
		MustChangePassword: mustChange,
		JWTSecret:          "test-jwt-secret",
		TokenVersion:       tokenVersion,
	}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	service := NewUserService()
	service.user = user
	return service
}
