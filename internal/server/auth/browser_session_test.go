package auth

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/op"
)

func TestBrowserSessionLifecycleAndCSRFBinder(t *testing.T) {
	resetBrowserSessionsForTest(t)
	sessionToken, csrfToken, expiresAt, err := CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("CreateBrowserSession() error = %v", err)
	}
	if sessionToken == "" || csrfToken == "" || sessionToken == csrfToken {
		t.Fatal("browser session and CSRF tokens must be independent non-empty values")
	}
	if !expiresAt.After(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("expiresAt = %v, want approximately one hour", expiresAt)
	}
	if !VerifyBrowserSession(sessionToken) {
		t.Fatal("new browser session was not valid")
	}
	if !VerifyBrowserSessionCSRF(sessionToken, csrfToken) {
		t.Fatal("bound CSRF token was rejected")
	}
	if VerifyBrowserSessionCSRF(sessionToken, csrfToken+"x") {
		t.Fatal("modified CSRF token was accepted")
	}
	if VerifyBrowserSession(sessionToken + "x") {
		t.Fatal("modified browser session token was accepted")
	}

	RevokeBrowserSession(sessionToken)
	if VerifyBrowserSession(sessionToken) || VerifyBrowserSessionCSRF(sessionToken, csrfToken) {
		t.Fatal("revoked browser session remained valid")
	}
}

func TestBrowserSessionRejectsExpiredAndOldTokenVersion(t *testing.T) {
	resetBrowserSessionsForTest(t)

	sessionToken, csrfToken, _, err := CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("CreateBrowserSession() error = %v", err)
	}
	key := sha256.Sum256([]byte(sessionToken))
	browserSessions.mu.Lock()
	session := browserSessions.sessions[key]
	session.expiresAt = time.Now().Add(-time.Second)
	browserSessions.sessions[key] = session
	browserSessions.mu.Unlock()
	if VerifyBrowserSessionCSRF(sessionToken, csrfToken) {
		t.Fatal("expired browser session passed CSRF verification")
	}
	if VerifyBrowserSession(sessionToken) {
		t.Fatal("expired browser session was accepted")
	}

	sessionToken, csrfToken, _, err = CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("CreateBrowserSession() error = %v", err)
	}
	key = sha256.Sum256([]byte(sessionToken))
	browserSessions.mu.Lock()
	session = browserSessions.sessions[key]
	session.tokenVersion--
	browserSessions.sessions[key] = session
	browserSessions.mu.Unlock()
	if VerifyBrowserSessionCSRF(sessionToken, csrfToken) {
		t.Fatal("old-version browser session passed CSRF verification")
	}
	if VerifyBrowserSession(sessionToken) {
		t.Fatal("browser session with an old token version was accepted")
	}
}

func TestBrowserSessionCapacityIsFailClosed(t *testing.T) {
	resetBrowserSessionsForTest(t)
	now := time.Now().Add(time.Hour)
	tokenVersion := op.UserGet().TokenVersion
	for i := 0; i < maxBrowserSessions; i++ {
		key := sha256.Sum256([]byte{byte(i), byte(i >> 8), byte(i >> 16)})
		browserSessions.sessions[key] = browserSession{
			expiresAt:    now,
			tokenVersion: tokenVersion,
			sessionHash:  key,
		}
	}
	_, _, _, err := CreateBrowserSession(60)
	if !errors.Is(err, ErrBrowserSessionCapacity) {
		t.Fatalf("CreateBrowserSession() error = %v, want capacity error", err)
	}
}

func resetBrowserSessionsForTest(t *testing.T) {
	t.Helper()
	browserSessions.mu.Lock()
	browserSessions.sessions = make(map[[sha256.Size]byte]browserSession)
	browserSessions.mu.Unlock()
	t.Cleanup(func() {
		browserSessions.mu.Lock()
		browserSessions.sessions = make(map[[sha256.Size]byte]browserSession)
		browserSessions.mu.Unlock()
	})
}
