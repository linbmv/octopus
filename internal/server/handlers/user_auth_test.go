package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/gin-gonic/gin"
	wa "github.com/go-webauthn/webauthn/webauthn"
)

func TestUserStatusAndPasswordErrorMapping(t *testing.T) {
	setupHandlerUser(t)

	statusResponse := invokeHandler(http.MethodGet, "/status", "", status)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	var statusBody model.UserStatusResponse
	decodeResponseData(t, statusResponse, &statusBody)
	if statusBody.Username != "admin" || !statusBody.MustChangePassword {
		t.Fatalf("status data = %#v", statusBody)
	}

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "wrong current password", body: `{"old_password":"wrong-password","new_password":"new-password"}`, want: http.StatusUnauthorized},
		{name: "invalid new password", body: `{"old_password":"initial-password","new_password":"short"}`, want: http.StatusBadRequest},
		{name: "unchanged password", body: `{"old_password":"initial-password","new_password":"initial-password"}`, want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/change-password", test.body, withUsername(changePassword, "admin"))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	stored := op.UserGet()
	if !stored.MustChangePassword || stored.TokenVersion != 0 {
		t.Fatalf("rejected changes mutated user: %#v", stored)
	}

	oldToken, _, err := auth.GenerateJWTToken(60)
	if err != nil {
		t.Fatalf("generate pre-change token: %v", err)
	}
	success := invokeHandler(
		http.MethodPost,
		"/change-password",
		`{"old_password":"initial-password","new_password":"new-password"}`,
		withUsername(changePassword, "admin"),
	)
	if success.Code != http.StatusOK {
		t.Fatalf("successful password change = %d; body=%s", success.Code, success.Body.String())
	}
	if auth.VerifyJWTToken(oldToken) {
		t.Fatal("pre-change JWT remained valid after password change")
	}
	if got := op.UserGet(); got.MustChangePassword || got.TokenVersion != 1 {
		t.Fatalf("successful password change state = %#v", got)
	}
}

func TestChangeUsernameInvalidatesExistingJWT(t *testing.T) {
	setupHandlerUser(t)
	oldToken, _, err := auth.GenerateJWTToken(60)
	if err != nil {
		t.Fatalf("generate pre-change token: %v", err)
	}

	unchanged := invokeHandler(
		http.MethodPost,
		"/change-username",
		`{"new_username":"admin","current_password":"initial-password"}`,
		withUsername(changeUsername, "admin"),
	)
	if unchanged.Code != http.StatusConflict {
		t.Fatalf("unchanged username = %d, want conflict; body=%s", unchanged.Code, unchanged.Body.String())
	}
	if !auth.VerifyJWTToken(oldToken) {
		t.Fatal("rejected username change invalidated token")
	}

	changed := invokeHandler(
		http.MethodPost,
		"/change-username",
		`{"new_username":"operator","current_password":"initial-password"}`,
		withUsername(changeUsername, "admin"),
	)
	if changed.Code != http.StatusOK {
		t.Fatalf("username change = %d; body=%s", changed.Code, changed.Body.String())
	}
	if auth.VerifyJWTToken(oldToken) {
		t.Fatal("pre-change JWT remained valid after username change")
	}
	if got := op.UserGet(); got.Username != "operator" || got.TokenVersion != 1 {
		t.Fatalf("changed user = %#v", got)
	}
}

func TestLoginAcceptsExplicitAndLegacyExpiryFields(t *testing.T) {
	setupHandlerUser(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "explicit minutes", body: `{"username":"admin","password":"initial-password","expires_in_minutes":60}`, want: http.StatusOK},
		{name: "legacy minutes", body: `{"username":"admin","password":"initial-password","expire":60}`, want: http.StatusOK},
		{name: "ambiguous fields", body: `{"username":"admin","password":"initial-password","expires_in_minutes":60,"expire":60}`, want: http.StatusBadRequest},
		{name: "invalid negative", body: `{"username":"admin","password":"initial-password","expires_in_minutes":-2}`, want: http.StatusBadRequest},
		{name: "cookie mode", body: `{"username":"admin","password":"initial-password","auth_mode":"cookie"}`, want: http.StatusOK},
		{name: "invalid auth mode", body: `{"username":"admin","password":"initial-password","auth_mode":"query"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := invokeHandler(http.MethodPost, "/login", test.body, login)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestLoginCookieModeDoesNotReturnJWTAndLegacyDefaultRemainsBearer(t *testing.T) {
	setupHandlerUser(t)

	cookieResponse := invokeHandler(
		http.MethodPost,
		"/login",
		`{"username":"admin","password":"initial-password","expires_in_minutes":60,"auth_mode":"cookie"}`,
		login,
	)
	if cookieResponse.Code != http.StatusOK {
		t.Fatalf("cookie login = %d; body=%s", cookieResponse.Code, cookieResponse.Body.String())
	}
	if got := cookieResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cookie login Cache-Control = %q, want no-store", got)
	}
	var cookieBody model.UserLoginResponse
	decodeResponseData(t, cookieResponse, &cookieBody)
	if cookieBody.AuthMode != model.UserAuthModeCookie || cookieBody.Token != "" {
		t.Fatalf("cookie login response exposed a token: %#v", cookieBody)
	}
	if bytes.Contains(cookieResponse.Body.Bytes(), []byte(`"token"`)) {
		t.Fatalf("cookie login JSON contains a token field: %s", cookieResponse.Body.String())
	}
	cookies := cookieResponse.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie login Set-Cookie count = %d, want 2", len(cookies))
	}
	byName := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
		if cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie attributes = %#v", cookie)
		}
	}
	if session := byName[middleware.AdminSessionCookieName]; session == nil || !session.HttpOnly || session.Value == "" {
		t.Fatalf("administrator session cookie = %#v", session)
	}
	if csrf := byName[middleware.CSRFCookieName]; csrf == nil || csrf.HttpOnly || csrf.Value == "" {
		t.Fatalf("CSRF cookie = %#v", csrf)
	}

	bearerResponse := invokeHandler(
		http.MethodPost,
		"/login",
		`{"username":"admin","password":"initial-password","expires_in_minutes":60}`,
		login,
	)
	if bearerResponse.Code != http.StatusOK {
		t.Fatalf("legacy login = %d; body=%s", bearerResponse.Code, bearerResponse.Body.String())
	}
	if got := bearerResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Bearer login Cache-Control = %q, want no-store", got)
	}
	var bearerBody model.UserLoginResponse
	decodeResponseData(t, bearerResponse, &bearerBody)
	if bearerBody.AuthMode != model.UserAuthModeBearer || bearerBody.Token == "" || !auth.VerifyJWTToken(bearerBody.Token) {
		t.Fatalf("legacy Bearer response = %#v", bearerBody)
	}
}

func TestLoginRequiresWebAuthnBeforeIssuingSession(t *testing.T) {
	setupHandlerUser(t)
	if err := db.GetDB().Model(&model.User{}).Where("username = ?", "admin").Update("must_change_password", false).Error; err != nil {
		t.Fatalf("clear forced password change: %v", err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatalf("reload user service: %v", err)
	}
	if err := op.WebAuthnCredentialCreate(t.Context(), "Test key", &wa.Credential{ID: []byte{1, 2, 3}, PublicKey: []byte{4, 5, 6}}); err != nil {
		t.Fatalf("create WebAuthn credential: %v", err)
	}
	previousConfig := conf.Current()
	config := previousConfig
	config.WebAuthn = conf.WebAuthn{
		Enabled:       true,
		RPID:          "localhost",
		RPDisplayName: "Octopus Test",
		RPOrigins:     []string{"http://localhost:8080"},
	}
	if err := conf.Set(config); err != nil {
		t.Fatalf("set WebAuthn config: %v", err)
	}
	t.Cleanup(func() {
		if err := conf.Set(previousConfig); err != nil {
			t.Errorf("restore config: %v", err)
		}
	})

	response := invokeHandler(
		http.MethodPost,
		"/login",
		`{"username":"admin","password":"initial-password","expires_in_minutes":60,"auth_mode":"cookie"}`,
		login,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("password factor status = %d; body=%s", response.Code, response.Body.String())
	}
	var body model.UserLoginResponse
	decodeResponseData(t, response, &body)
	if !body.WebAuthnRequired || body.WebAuthnTransaction == "" || body.WebAuthnOptions == nil {
		t.Fatalf("password factor response = %#v", body)
	}
	if body.Token != "" || body.ExpireAt != "" || len(response.Result().Cookies()) != 0 {
		t.Fatalf("password factor issued credentials before WebAuthn: body=%#v cookies=%#v", body, response.Result().Cookies())
	}
}

func TestLogoutRevokesOnlyCurrentBrowserSessionAndClearsCookies(t *testing.T) {
	setupHandlerUser(t)
	firstSession, firstCSRF, _, err := auth.CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	secondSession, _, _, err := auth.CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	t.Cleanup(func() { auth.RevokeBrowserSession(secondSession) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/user/logout", middleware.Auth(), logout)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/user/logout", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.CSRFHeaderName, firstCSRF)
	request.AddCookie(&http.Cookie{Name: middleware.AdminSessionCookieName, Value: firstSession})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: firstCSRF})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("logout status = %d; body=%s", response.Code, response.Body.String())
	}
	if auth.VerifyBrowserSession(firstSession) {
		t.Fatal("logged out browser session remained valid")
	}
	if !auth.VerifyBrowserSession(secondSession) {
		t.Fatal("logging out one browser session invalidated another session")
	}
	cleared := response.Result().Cookies()
	if len(cleared) != 2 || cleared[0].MaxAge >= 0 || cleared[1].MaxAge >= 0 {
		t.Fatalf("logout clearing cookies = %#v", cleared)
	}
}

func setupHandlerUser(t *testing.T) {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "user-handler.db"), false); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := model.User{
		Username:           "admin",
		Password:           "initial-password",
		MustChangePassword: true,
		JWTSecret:          "handler-test-jwt-secret",
	}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatalf("init user service: %v", err)
	}
}

func withUsername(handler gin.HandlerFunc, username string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("username", username)
		handler(c)
	}
}
