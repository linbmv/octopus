package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/go-webauthn/webauthn/protocol"
	wa "github.com/go-webauthn/webauthn/webauthn"
)

const (
	webAuthnCeremonyTTL      = 5 * time.Minute
	maxPendingWebAuthn       = 1024
	webAuthnRegistrationKind = "registration"
	webAuthnLoginKind        = "login"
)

var (
	ErrWebAuthnDisabled          = errors.New("WebAuthn is disabled")
	ErrWebAuthnNotConfigured     = errors.New("WebAuthn is not configured")
	ErrWebAuthnCeremonyExpired   = errors.New("WebAuthn ceremony expired or not found")
	ErrWebAuthnCeremonyBinding   = errors.New("WebAuthn ceremony client binding mismatch")
	ErrWebAuthnCeremonyCapacity  = errors.New("WebAuthn ceremony capacity reached")
	ErrWebAuthnCredentialsAbsent = errors.New("no WebAuthn credentials registered")

	pendingWebAuthnMu sync.Mutex
	pendingWebAuthn   = make(map[string]pendingWebAuthnCeremony)
)

type WebAuthnBeginResponse struct {
	Transaction string `json:"transaction"`
	PublicKey   any    `json:"public_key"`
}

type WebAuthnLoginCompletion struct {
	ExpiresInMinutes int
	AuthMode         string
}

type pendingWebAuthnCeremony struct {
	Kind             string
	Session          wa.SessionData
	UserID           uint
	TokenVersion     int
	Binding          string
	CredentialName   string
	ExpiresInMinutes int
	AuthMode         string
	ExpiresAt        time.Time
}

func WebAuthnEnabled() bool {
	return conf.Current().WebAuthn.Enabled
}

func WebAuthnRequestBinding(clientIP, userAgent string) string {
	digest := sha256.Sum256([]byte(clientIP + "\x00" + userAgent))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func BeginWebAuthnRegistration(ctx context.Context, binding, credentialName string) (*WebAuthnBeginResponse, error) {
	engine, err := newWebAuthnEngine()
	if err != nil {
		return nil, err
	}
	user, err := op.LoadWebAuthnUser(ctx)
	if err != nil {
		return nil, err
	}
	creation, session, err := engine.BeginRegistration(
		user,
		wa.WithExclusions(wa.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
		wa.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("begin WebAuthn registration: %w", err)
	}
	currentUser := op.UserGet()
	transaction, err := storePendingWebAuthn(pendingWebAuthnCeremony{
		Kind:           webAuthnRegistrationKind,
		Session:        *session,
		UserID:         currentUser.ID,
		TokenVersion:   currentUser.TokenVersion,
		Binding:        binding,
		CredentialName: credentialName,
		ExpiresAt:      time.Now().Add(webAuthnCeremonyTTL),
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthnBeginResponse{Transaction: transaction, PublicKey: creation}, nil
}

func FinishWebAuthnRegistration(ctx context.Context, binding, transaction string, request *http.Request) error {
	pending, err := takePendingWebAuthn(transaction, webAuthnRegistrationKind, binding)
	if err != nil {
		return err
	}
	currentUser := op.UserGet()
	if pending.UserID != currentUser.ID || pending.TokenVersion != currentUser.TokenVersion {
		return ErrWebAuthnCeremonyBinding
	}
	engine, err := newWebAuthnEngine()
	if err != nil {
		return err
	}
	user, err := op.LoadWebAuthnUser(ctx)
	if err != nil {
		return err
	}
	credential, err := engine.FinishRegistration(user, pending.Session, request)
	if err != nil {
		return fmt.Errorf("finish WebAuthn registration: %w", err)
	}
	return op.WebAuthnCredentialCreate(ctx, pending.CredentialName, credential)
}

func BeginWebAuthnLogin(ctx context.Context, binding string, expiresInMinutes int, authMode string) (*WebAuthnBeginResponse, error) {
	engine, err := newWebAuthnEngine()
	if err != nil {
		return nil, err
	}
	user, err := op.LoadWebAuthnUser(ctx)
	if err != nil {
		return nil, err
	}
	if len(user.WebAuthnCredentials()) == 0 {
		return nil, ErrWebAuthnCredentialsAbsent
	}
	assertion, session, err := engine.BeginLogin(user, wa.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("begin WebAuthn login: %w", err)
	}
	currentUser := op.UserGet()
	transaction, err := storePendingWebAuthn(pendingWebAuthnCeremony{
		Kind:             webAuthnLoginKind,
		Session:          *session,
		UserID:           currentUser.ID,
		TokenVersion:     currentUser.TokenVersion,
		Binding:          binding,
		ExpiresInMinutes: expiresInMinutes,
		AuthMode:         authMode,
		ExpiresAt:        time.Now().Add(webAuthnCeremonyTTL),
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthnBeginResponse{Transaction: transaction, PublicKey: assertion}, nil
}

func FinishWebAuthnLogin(ctx context.Context, binding, transaction string, request *http.Request) (WebAuthnLoginCompletion, error) {
	pending, err := takePendingWebAuthn(transaction, webAuthnLoginKind, binding)
	if err != nil {
		return WebAuthnLoginCompletion{}, err
	}
	currentUser := op.UserGet()
	if pending.UserID != currentUser.ID || pending.TokenVersion != currentUser.TokenVersion {
		return WebAuthnLoginCompletion{}, ErrWebAuthnCeremonyBinding
	}
	engine, err := newWebAuthnEngine()
	if err != nil {
		return WebAuthnLoginCompletion{}, err
	}
	user, err := op.LoadWebAuthnUser(ctx)
	if err != nil {
		return WebAuthnLoginCompletion{}, err
	}
	credential, err := engine.FinishLogin(user, pending.Session, request)
	if err != nil {
		return WebAuthnLoginCompletion{}, fmt.Errorf("finish WebAuthn login: %w", err)
	}
	if err := op.WebAuthnCredentialUpdate(ctx, credential); err != nil {
		return WebAuthnLoginCompletion{}, err
	}
	return WebAuthnLoginCompletion{ExpiresInMinutes: pending.ExpiresInMinutes, AuthMode: pending.AuthMode}, nil
}

func newWebAuthnEngine() (*wa.WebAuthn, error) {
	config := conf.Current().WebAuthn
	if !config.Enabled {
		return nil, ErrWebAuthnDisabled
	}
	if config.RPID == "" || len(config.RPOrigins) == 0 {
		return nil, ErrWebAuthnNotConfigured
	}
	engine, err := wa.New(&wa.Config{
		RPID:          config.RPID,
		RPDisplayName: config.RPDisplayName,
		RPOrigins:     config.RPOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: wa.TimeoutsConfig{
			Login:        wa.TimeoutConfig{Enforce: true, Timeout: webAuthnCeremonyTTL, TimeoutUVD: webAuthnCeremonyTTL},
			Registration: wa.TimeoutConfig{Enforce: true, Timeout: webAuthnCeremonyTTL, TimeoutUVD: webAuthnCeremonyTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure WebAuthn: %w", err)
	}
	return engine, nil
}

func storePendingWebAuthn(pending pendingWebAuthnCeremony) (string, error) {
	now := time.Now()
	pendingWebAuthnMu.Lock()
	defer pendingWebAuthnMu.Unlock()
	for id, existing := range pendingWebAuthn {
		if !now.Before(existing.ExpiresAt) {
			delete(pendingWebAuthn, id)
		}
	}
	if len(pendingWebAuthn) >= maxPendingWebAuthn {
		return "", ErrWebAuthnCeremonyCapacity
	}
	for attempts := 0; attempts < 4; attempts++ {
		buffer := make([]byte, 32)
		if _, err := rand.Read(buffer); err != nil {
			return "", fmt.Errorf("generate WebAuthn transaction: %w", err)
		}
		id := base64.RawURLEncoding.EncodeToString(buffer)
		if _, exists := pendingWebAuthn[id]; exists {
			continue
		}
		pendingWebAuthn[id] = pending
		return id, nil
	}
	return "", ErrWebAuthnCeremonyCapacity
}

func takePendingWebAuthn(transaction, kind, binding string) (pendingWebAuthnCeremony, error) {
	pendingWebAuthnMu.Lock()
	defer pendingWebAuthnMu.Unlock()
	pending, ok := pendingWebAuthn[transaction]
	if ok {
		delete(pendingWebAuthn, transaction)
	}
	if !ok || !time.Now().Before(pending.ExpiresAt) || pending.Kind != kind {
		return pendingWebAuthnCeremony{}, ErrWebAuthnCeremonyExpired
	}
	if pending.Binding != binding {
		return pendingWebAuthnCeremony{}, ErrWebAuthnCeremonyBinding
	}
	return pending, nil
}

func WebAuthnCredentialInfos(ctx context.Context) ([]model.WebAuthnCredentialInfo, error) {
	return op.WebAuthnCredentialList(ctx)
}
