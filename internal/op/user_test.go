package op

import (
	"testing"

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
