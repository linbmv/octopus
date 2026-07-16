package op

import (
	"context"
	"errors"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	wa "github.com/go-webauthn/webauthn/webauthn"
)

func TestWebAuthnCredentialLifecycle(t *testing.T) {
	initTestDB(t)
	user := model.User{Username: "admin", Password: "password", JWTSecret: "stable-random-secret"}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	userService.mu.Lock()
	previous := userService.user
	userService.user = user
	userService.mu.Unlock()
	t.Cleanup(func() {
		userService.mu.Lock()
		userService.user = previous
		userService.mu.Unlock()
	})

	credential := &wa.Credential{ID: []byte{1, 2, 3}, PublicKey: []byte{4, 5, 6}}
	if err := WebAuthnCredentialCreate(context.Background(), "Laptop", credential); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if err := WebAuthnCredentialCreate(context.Background(), "Duplicate", credential); err == nil {
		t.Fatal("duplicate credential was accepted")
	}

	items, err := WebAuthnCredentialList(context.Background())
	if err != nil || len(items) != 1 || items[0].Name != "Laptop" {
		t.Fatalf("credential list = %#v, %v", items, err)
	}
	loaded, err := LoadWebAuthnUser(context.Background())
	if err != nil || len(loaded.WebAuthnCredentials()) != 1 {
		t.Fatalf("loaded WebAuthn user = %#v, %v", loaded, err)
	}
	firstHandle := append([]byte(nil), loaded.WebAuthnID()...)
	if string(firstHandle) != string(loaded.WebAuthnID()) {
		t.Fatal("WebAuthn user handle is not stable")
	}

	credential.Authenticator.SignCount = 7
	if err := WebAuthnCredentialUpdate(context.Background(), credential); err != nil {
		t.Fatalf("update credential: %v", err)
	}
	loaded, err = LoadWebAuthnUser(context.Background())
	if err != nil || loaded.WebAuthnCredentials()[0].Authenticator.SignCount != 7 {
		t.Fatalf("updated credential = %#v, %v", loaded.WebAuthnCredentials(), err)
	}

	if err := WebAuthnCredentialDelete(context.Background(), items[0].ID); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if err := WebAuthnCredentialDelete(context.Background(), items[0].ID); !errors.Is(err, ErrWebAuthnCredentialNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}
