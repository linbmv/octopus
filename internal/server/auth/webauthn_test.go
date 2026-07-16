package auth

import (
	"testing"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

func TestWebAuthnRequestBinding(t *testing.T) {
	left := WebAuthnRequestBinding("203.0.113.7", "test-agent")
	if left == "" || left != WebAuthnRequestBinding("203.0.113.7", "test-agent") {
		t.Fatal("WebAuthn request binding is not stable")
	}
	if left == WebAuthnRequestBinding("203.0.113.8", "test-agent") {
		t.Fatal("WebAuthn request binding ignored client IP")
	}
	if left == WebAuthnRequestBinding("203.0.113.7", "another-agent") {
		t.Fatal("WebAuthn request binding ignored user agent")
	}
}

func TestPendingWebAuthnCeremonyIsBoundAndSingleUse(t *testing.T) {
	pendingWebAuthnMu.Lock()
	pendingWebAuthn = make(map[string]pendingWebAuthnCeremony)
	pendingWebAuthnMu.Unlock()

	id, err := storePendingWebAuthn(pendingWebAuthnCeremony{
		Kind:      webAuthnLoginKind,
		Session:   wa.SessionData{Challenge: "challenge"},
		Binding:   "binding",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("storePendingWebAuthn() error = %v", err)
	}
	pending, err := takePendingWebAuthn(id, webAuthnLoginKind, "binding")
	if err != nil || pending.Session.Challenge != "challenge" {
		t.Fatalf("takePendingWebAuthn() = %#v, %v", pending, err)
	}
	if _, err := takePendingWebAuthn(id, webAuthnLoginKind, "binding"); err != ErrWebAuthnCeremonyExpired {
		t.Fatalf("replayed ceremony error = %v, want expired", err)
	}

	id, err = storePendingWebAuthn(pendingWebAuthnCeremony{Kind: webAuthnRegistrationKind, Binding: "binding", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("store bound ceremony: %v", err)
	}
	if _, err := takePendingWebAuthn(id, webAuthnRegistrationKind, "attacker"); err != ErrWebAuthnCeremonyBinding {
		t.Fatalf("binding mismatch error = %v", err)
	}
}

func TestPendingWebAuthnCeremonyExpires(t *testing.T) {
	pendingWebAuthnMu.Lock()
	pendingWebAuthn = make(map[string]pendingWebAuthnCeremony)
	pendingWebAuthnMu.Unlock()
	id, err := storePendingWebAuthn(pendingWebAuthnCeremony{Kind: webAuthnLoginKind, Binding: "binding", ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatalf("store expired ceremony: %v", err)
	}
	if _, err := takePendingWebAuthn(id, webAuthnLoginKind, "binding"); err != ErrWebAuthnCeremonyExpired {
		t.Fatalf("expired ceremony error = %v", err)
	}
}
