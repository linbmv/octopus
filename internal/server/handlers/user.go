package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.RequireJSON()).
		Use(middleware.RateLimit(5, time.Minute)).
		AddRoute(
			router.NewRoute("/login", http.MethodPost).
				Handle(login),
		).
		AddRoute(
			router.NewRoute("/login/webauthn/finish", http.MethodPost).
				Handle(finishWebAuthnLogin),
		)
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/change-password", http.MethodPost).
				Handle(changePassword),
		).
		AddRoute(
			router.NewRoute("/change-username", http.MethodPost).
				Handle(changeUsername),
		).
		AddRoute(
			router.NewRoute("/status", http.MethodGet).
				Handle(status),
		).
		AddRoute(
			router.NewRoute("/logout", http.MethodPost).
				Handle(logout),
		).
		AddRoute(
			router.NewRoute("/webauthn/register/begin", http.MethodPost).
				Handle(beginWebAuthnRegistration),
		).
		AddRoute(
			router.NewRoute("/webauthn/register/finish", http.MethodPost).
				Handle(finishWebAuthnRegistration),
		).
		AddRoute(
			router.NewRoute("/webauthn/credentials", http.MethodGet).
				Handle(listWebAuthnCredentials),
		).
		AddRoute(
			router.NewRoute("/webauthn/delete", http.MethodPost).
				Handle(deleteWebAuthnCredential),
		)
}

func login(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	var user model.UserLogin
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.UserVerify(user.Username, user.Password); err != nil {
		middleware.AuditLog(c, middleware.EventUserLoginFailed, map[string]interface{}{
			"username": user.Username,
			"reason":   "invalid_credentials",
		})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	expiresInMinutes, err := user.RequestedExpiryMinutes()
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	authMode, err := user.RequestedAuthMode()
	if err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}

	response := model.UserLoginResponse{
		MustChangePassword: op.UserMustChangePassword(),
		AuthMode:           authMode,
	}
	if auth.WebAuthnEnabled() && !response.MustChangePassword {
		credentialCount, countErr := op.WebAuthnCredentialCount(c.Request.Context())
		if countErr != nil {
			resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
			return
		}
		if credentialCount > 0 {
			challenge, beginErr := auth.BeginWebAuthnLogin(
				c.Request.Context(),
				webAuthnBinding(c),
				expiresInMinutes,
				authMode,
			)
			if beginErr != nil {
				if errors.Is(beginErr, auth.ErrWebAuthnCeremonyCapacity) {
					resp.Error(c, http.StatusServiceUnavailable, resp.ErrInternalServer)
				} else {
					resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
				}
				return
			}
			response.WebAuthnRequired = true
			response.WebAuthnTransaction = challenge.Transaction
			response.WebAuthnOptions = challenge.PublicKey
			middleware.AuditLog(c, middleware.EventUserLogin, map[string]interface{}{
				"username":  user.Username,
				"auth_mode": authMode,
				"factor":    "password_pending_webauthn",
			})
			resp.Success(c, response)
			return
		}
	}
	completeLogin(c, expiresInMinutes, authMode, response.MustChangePassword, false)
}

func completeLogin(c *gin.Context, expiresInMinutes int, authMode string, mustChangePassword, webAuthnVerified bool) {
	response := model.UserLoginResponse{MustChangePassword: mustChangePassword, AuthMode: authMode}
	switch authMode {
	case model.UserAuthModeCookie:
		sessionToken, csrfToken, expiresAt, createErr := auth.CreateBrowserSession(expiresInMinutes)
		if createErr != nil {
			if errors.Is(createErr, auth.ErrInvalidTokenExpiry) {
				resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
				return
			}
			if errors.Is(createErr, auth.ErrBrowserSessionCapacity) {
				resp.Error(c, http.StatusServiceUnavailable, resp.ErrInternalServer)
				return
			}
			resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
			return
		}
		if oldSession, cookieErr := c.Cookie(middleware.AdminSessionCookieName); cookieErr == nil {
			auth.RevokeBrowserSession(oldSession)
		}
		middleware.SetBrowserSessionCookies(c, sessionToken, csrfToken, expiresAt)
		response.ExpireAt = expiresAt.Format(time.RFC3339)
	case model.UserAuthModeBearer:
		token, expire, generateErr := auth.GenerateJWTToken(expiresInMinutes)
		if generateErr != nil {
			if errors.Is(generateErr, auth.ErrInvalidTokenExpiry) {
				resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
				return
			}
			resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
			return
		}
		response.Token = token
		response.ExpireAt = expire
	default:
		// RequestedAuthMode has already constrained this value.
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if response.ExpireAt == "" {
		// Defensive guard: no successful login response may carry an ambiguous
		// non-expiring session.
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	middleware.AuditLog(c, middleware.EventUserLogin, map[string]interface{}{
		"username":                     op.UserGet().Username,
		"auth_mode":                    authMode,
		"webauthn_verified":            webAuthnVerified,
		"requested_expires_in_minutes": expiresInMinutes,
		"expire_at":                    response.ExpireAt,
	})
	resp.Success(c, response)
}

func finishWebAuthnLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	transaction := c.GetHeader("X-Octopus-WebAuthn-Transaction")
	if transaction == "" {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	completion, err := auth.FinishWebAuthnLogin(c.Request.Context(), webAuthnBinding(c), transaction, c.Request)
	if err != nil {
		middleware.AuditLog(c, middleware.EventUserLoginFailed, map[string]interface{}{"reason": "webauthn_failed"})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	completeLogin(c, completion.ExpiresInMinutes, completion.AuthMode, false, true)
}

func beginWebAuthnRegistration(c *gin.Context) {
	var request model.WebAuthnRegistrationBegin
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	user := op.UserGet()
	if err := op.UserVerify(user.Username, request.CurrentPassword); err != nil {
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	challenge, err := auth.BeginWebAuthnRegistration(c.Request.Context(), webAuthnBinding(c), request.Name)
	if err != nil {
		if errors.Is(err, auth.ErrWebAuthnDisabled) || errors.Is(err, auth.ErrWebAuthnNotConfigured) {
			resp.Error(c, http.StatusConflict, "WebAuthn is not enabled")
			return
		}
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	resp.Success(c, challenge)
}

func finishWebAuthnRegistration(c *gin.Context) {
	transaction := c.GetHeader("X-Octopus-WebAuthn-Transaction")
	if transaction == "" {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidParam)
		return
	}
	if err := auth.FinishWebAuthnRegistration(c.Request.Context(), webAuthnBinding(c), transaction, c.Request); err != nil {
		if errors.Is(err, op.ErrWebAuthnCredentialExists) {
			resp.Error(c, http.StatusConflict, "WebAuthn credential already exists")
			return
		}
		resp.Error(c, http.StatusBadRequest, "WebAuthn registration failed")
		return
	}
	middleware.AuditLog(c, middleware.EventUserLogin, map[string]interface{}{"operation": "webauthn_register"})
	resp.Success(c, "WebAuthn credential registered")
}

func listWebAuthnCredentials(c *gin.Context) {
	credentials, err := auth.WebAuthnCredentialInfos(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	resp.Success(c, credentials)
}

func deleteWebAuthnCredential(c *gin.Context) {
	var request model.WebAuthnCredentialDelete
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	user := op.UserGet()
	if err := op.UserVerify(user.Username, request.CurrentPassword); err != nil {
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	if err := op.WebAuthnCredentialDelete(c.Request.Context(), request.ID); err != nil {
		if errors.Is(err, op.ErrWebAuthnCredentialNotFound) {
			resp.Error(c, http.StatusNotFound, resp.ErrResourceNotFound)
			return
		}
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	middleware.AuditLog(c, middleware.EventUserLogin, map[string]interface{}{"operation": "webauthn_delete", "credential_id": request.ID})
	resp.Success(c, "WebAuthn credential deleted")
}

func webAuthnBinding(c *gin.Context) string {
	return auth.WebAuthnRequestBinding(c.ClientIP(), c.Request.UserAgent())
}

func changePassword(c *gin.Context) {
	var user model.UserChangePassword
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	username, _ := c.Get("username")
	usernameStr, _ := username.(string)

	if err := op.UserChangePasswordContext(c.Request.Context(), user.OldPassword, user.NewPassword); err != nil {
		switch {
		case errors.Is(err, op.ErrInvalidCurrentPassword):
			middleware.AuditLog(c, middleware.EventSensitiveOperationDenied, map[string]interface{}{
				"username":  usernameStr,
				"operation": "password_change",
				"reason":    "invalid_current_password",
			})
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		case errors.Is(err, op.ErrInvalidPassword):
			resp.Error(c, http.StatusBadRequest, resp.ErrPasswordPolicy)
		case errors.Is(err, op.ErrPasswordUnchanged):
			resp.Error(c, http.StatusConflict, resp.ErrPasswordUnchanged)
		default:
			resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		}
		return
	}
	middleware.AuditLog(c, middleware.EventPasswordChange, map[string]interface{}{
		"username": username,
	})
	middleware.RevokeRequestBrowserSession(c)
	resp.Success(c, "password changed successfully")
}

func changeUsername(c *gin.Context) {
	var user model.UserChangeUsername
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	oldUsername, _ := c.Get("username")
	usernameStr, _ := oldUsername.(string)
	if err := op.UserVerify(usernameStr, user.CurrentPassword); err != nil {
		middleware.AuditLog(c, middleware.EventUsernameChangeFailed, map[string]interface{}{
			"username": usernameStr,
			"reason":   "invalid_password",
		})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	if err := op.UserChangeUsernameContext(c.Request.Context(), user.NewUsername); err != nil {
		if errors.Is(err, op.ErrUsernameUnchanged) {
			resp.Error(c, http.StatusConflict, resp.ErrUsernameUnchanged)
		} else {
			resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		}
		return
	}
	middleware.AuditLog(c, middleware.EventUsernameChange, map[string]interface{}{
		"old_username": oldUsername,
		"new_username": user.NewUsername,
	})
	middleware.RevokeRequestBrowserSession(c)
	resp.Success(c, "username changed successfully")
}

func logout(c *gin.Context) {
	username, _ := c.Get("username")
	middleware.RevokeRequestBrowserSession(c)
	middleware.AuditLog(c, middleware.EventUserLogout, map[string]interface{}{
		"username": username,
	})
	resp.Success(c, "logged out successfully")
}

func status(c *gin.Context) {
	user := op.UserGet()
	var credentialCount int64
	if auth.WebAuthnEnabled() {
		var err error
		credentialCount, err = op.WebAuthnCredentialCount(c.Request.Context())
		if err != nil {
			resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
			return
		}
	}
	resp.Success(c, model.UserStatusResponse{
		Username:            user.Username,
		MustChangePassword:  user.MustChangePassword,
		WebAuthnEnabled:     auth.WebAuthnEnabled(),
		WebAuthnCredentials: credentialCount,
	})
}
