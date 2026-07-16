package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
)

const (
	changePasswordPath = "/api/v1/user/change-password"
	userStatusPath     = "/api/v1/user/status"
	userLogoutPath     = "/api/v1/user/logout"
	authMethodContext  = "admin_auth_method"
	authMethodBearer   = "bearer"
	authMethodCookie   = "cookie"
)

func allowedWhilePasswordChangeRequired(path string) bool {
	return path == changePasswordPath || path == userStatusPath || path == userLogoutPath
}

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Management responses can contain channel credentials, API keys and
		// account state. Keep them out of browser and intermediary caches even
		// after a session is revoked; React Query provides the intended in-memory
		// client cache instead.
		c.Header("Cache-Control", "no-store")
		authMethod := ""
		authorization := c.GetHeader("Authorization")
		if authorization != "" {
			token, ok := parseBearerAuthorization(authorization)
			if !ok || !auth.VerifyJWTToken(token) {
				resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
				c.Abort()
				return
			}
			authMethod = authMethodBearer
		} else {
			sessionToken, err := c.Cookie(AdminSessionCookieName)
			if err != nil {
				resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
				c.Abort()
				return
			}
			if requestMethodNeedsCSRF(c.Request.Method) {
				sessionValid, csrfValid := validateCookieCSRF(c, sessionToken)
				if !sessionValid {
					resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
					c.Abort()
					return
				}
				if !csrfValid {
					resp.ErrorWithCode(c, http.StatusForbidden, resp.CodeCSRFValidationFailed, resp.ErrCSRFValidation)
					c.Abort()
					return
				}
			} else if !auth.VerifyBrowserSession(sessionToken) {
				resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
				c.Abort()
				return
			}
			authMethod = authMethodCookie
		}
		user := op.UserGet()
		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set(authMethodContext, authMethod)
		c.Request = c.Request.WithContext(log.WithUserID(c.Request.Context(), user.ID))
		// A forced-password session may inspect its status and change the
		// password, but no other protected resource is exposed.
		if op.UserMustChangePassword() && !allowedWhilePasswordChangeRequired(c.Request.URL.Path) {
			resp.ErrorWithCode(c, http.StatusForbidden, resp.CodePasswordChangeRequired, resp.ErrPasswordChange)
			c.Abort()
			return
		}
		c.Next()
	}
}

func parseBearerAuthorization(value string) (string, bool) {
	fields := strings.Fields(value)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Model discovery and API-key dashboards are credential-dependent. In
		// particular, /v1/models is filtered by SupportedModels, so a shared
		// browser/proxy cache must never reuse one key's result for another key.
		c.Header("Cache-Control", "no-store")
		var apiKey string
		var requestType string

		if key := c.Request.Header.Get("x-api-key"); key != "" {
			apiKey = key
			requestType = "anthropic"
		} else if auth := c.Request.Header.Get("Authorization"); auth != "" {
			apiKey = strings.TrimPrefix(auth, "Bearer ")
			requestType = "openai"
		} else if key := c.Request.Header.Get("x-goog-api-key"); key != "" {
			apiKey = key
			requestType = "gemini"
		} else if key := c.Query("key"); key != "" {
			apiKey = key
			requestType = "gemini"
		}

		if apiKey == "" {
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}

		if !strings.HasPrefix(apiKey, "sk-"+conf.APP_NAME+"-") {
			resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
			c.Abort()
			return
		}
		var apiKeyObj model.APIKey
		var reservation *op.APIKeyCostReservation
		var err error
		if requestMayAccrueCost(c.Request) {
			apiKeyObj, reservation, err = op.APIKeyCostReserve(apiKey)
		} else {
			apiKeyObj, err = op.APIKeyCostCheck(apiKey)
		}
		if reservation != nil {
			// This defer runs when the handler completes, its request context is
			// canceled and the handler returns, or a panic unwinds to Gin's recovery
			// middleware. Release is idempotent for defensive callers.
			defer reservation.Release()
		}
		if err != nil {
			if !errors.Is(err, op.ErrAPIKeyMaxCostReached) && !errors.Is(err, op.ErrAPIKeyMaxCostReserved) {
				resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
				c.Abort()
				return
			}
		}
		if !apiKeyObj.Enabled {
			resp.Error(c, http.StatusUnauthorized, "API key is disabled")
			c.Abort()
			return
		}
		if apiKeyObj.ExpireAt > 0 && apiKeyObj.ExpireAt < time.Now().Unix() {
			resp.Error(c, http.StatusUnauthorized, "API key has expired")
			c.Abort()
			return
		}
		if err != nil {
			if errors.Is(err, op.ErrAPIKeyMaxCostReserved) {
				c.Header("Retry-After", "1")
			}
			resp.Error(c, http.StatusTooManyRequests, err.Error())
			c.Abort()
			return
		}
		c.Set("request_type", requestType)
		c.Set("supported_models", apiKeyObj.SupportedModels)
		c.Set("api_key_id", apiKeyObj.ID)
		c.Next()
	}
}

func requestMayAccrueCost(request *http.Request) bool {
	if request == nil {
		return false
	}
	// Every currently registered relay/generation endpoint uses POST. Treat any
	// future non-safe method conservatively as potentially billable while keeping
	// model discovery, login, stats, HEAD and preflight usable during generation.
	return request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions
}
