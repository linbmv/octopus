package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/op"
)

const (
	browserSessionRandomBytes = 32
	maxBrowserSessions        = 4096
)

var ErrBrowserSessionCapacity = errors.New("browser session capacity reached")

type browserSession struct {
	expiresAt    time.Time
	tokenVersion int
	sessionHash  [sha256.Size]byte
	csrfHash     [sha256.Size]byte
}

type browserSessionStore struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]browserSession
}

var browserSessions = browserSessionStore{
	sessions: make(map[[sha256.Size]byte]browserSession),
}

// CreateBrowserSession creates an opaque, server-side browser session. The
// session cookie never contains an administrator JWT, and the independently
// generated CSRF token is bound to this session in the server-side store.
// Sessions intentionally do not survive a process restart; Octopus currently
// supports a single application instance and a restart requires re-login.
func CreateBrowserSession(expiresMin int) (sessionToken, csrfToken string, expiresAt time.Time, err error) {
	lifetime, err := tokenLifetime(expiresMin)
	if err != nil {
		return "", "", time.Time{}, err
	}
	sessionToken, err = randomBrowserToken()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate browser session: %w", err)
	}
	csrfToken, err = randomBrowserToken()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate CSRF token: %w", err)
	}

	now := time.Now()
	expiresAt = now.Add(lifetime)
	user := op.UserGet()
	key := sha256.Sum256([]byte(sessionToken))
	session := browserSession{
		expiresAt:    expiresAt,
		tokenVersion: user.TokenVersion,
		sessionHash:  key,
		csrfHash:     sha256.Sum256([]byte(csrfToken)),
	}

	browserSessions.mu.Lock()
	pruneBrowserSessionsLocked(now, user.TokenVersion)
	if len(browserSessions.sessions) >= maxBrowserSessions {
		browserSessions.mu.Unlock()
		return "", "", time.Time{}, ErrBrowserSessionCapacity
	}
	browserSessions.sessions[key] = session
	browserSessions.mu.Unlock()

	return sessionToken, csrfToken, expiresAt, nil
}

// VerifyBrowserSession checks expiry and the administrator token version. A
// password or username change increments the version and therefore invalidates
// every previously issued browser session as well as every JWT.
func VerifyBrowserSession(sessionToken string) bool {
	sessionValid, _ := VerifyBrowserSessionRequest(sessionToken, "")
	return sessionValid
}

// VerifyBrowserSessionCSRF compares the presented token with the value bound
// to the browser session. The hash comparison is constant-time.
func VerifyBrowserSessionCSRF(sessionToken, csrfToken string) bool {
	sessionValid, csrfValid := VerifyBrowserSessionRequest(sessionToken, csrfToken)
	return sessionValid && csrfValid
}

// VerifyBrowserSessionRequest evaluates existence, expiry, token version and
// the optional CSRF binding while holding one session-store lock. This
// avoids a request passing session validation and then racing expiry/revocation
// before a second CSRF lookup.
func VerifyBrowserSessionRequest(sessionToken, csrfToken string) (sessionValid, csrfValid bool) {
	if sessionToken == "" {
		return false, false
	}
	user := op.UserGet()
	key := sha256.Sum256([]byte(sessionToken))
	presentedCSRFHash := sha256.Sum256([]byte(csrfToken))
	now := time.Now()

	browserSessions.mu.Lock()
	defer browserSessions.mu.Unlock()
	session, ok := browserSessions.sessions[key]
	if !ok {
		return false, false
	}
	if subtle.ConstantTimeCompare(session.sessionHash[:], key[:]) != 1 ||
		!now.Before(session.expiresAt) || session.tokenVersion != user.TokenVersion {
		delete(browserSessions.sessions, key)
		return false, false
	}
	if csrfToken == "" {
		return true, false
	}
	return true, subtle.ConstantTimeCompare(session.csrfHash[:], presentedCSRFHash[:]) == 1
}

// RevokeBrowserSession invalidates only the selected browser session. Other
// browser sessions and API Bearer tokens remain valid.
func RevokeBrowserSession(sessionToken string) {
	if sessionToken == "" {
		return
	}
	key := sha256.Sum256([]byte(sessionToken))
	browserSessions.mu.Lock()
	delete(browserSessions.sessions, key)
	browserSessions.mu.Unlock()
}

func randomBrowserToken() (string, error) {
	random := make([]byte, browserSessionRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func pruneBrowserSessionsLocked(now time.Time, tokenVersion int) {
	for key, session := range browserSessions.sessions {
		if !now.Before(session.expiresAt) || session.tokenVersion != tokenVersion {
			delete(browserSessions.sessions, key)
		}
	}
}
