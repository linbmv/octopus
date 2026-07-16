package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/gin-gonic/gin"
)

func TestCookieAuthRequiresSessionBoundCSRFOnUnsafeMethods(t *testing.T) {
	bearerToken := setupForcedPasswordUser(t)
	sessionToken, csrfToken, _, err := auth.CreateBrowserSession(60)
	if err != nil {
		t.Fatalf("CreateBrowserSession() error = %v", err)
	}
	t.Cleanup(func() { auth.RevokeBrowserSession(sessionToken) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET(userStatusPath, Auth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.POST(changePasswordPath, Auth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, userStatusPath, nil)
	request.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("safe cookie request status = %d; body=%s", response.Code, response.Body.String())
	}

	for _, test := range []struct {
		name       string
		csrfCookie string
		csrfHeader string
		want       int
	}{
		{name: "missing", want: http.StatusForbidden},
		{name: "mismatched double submit", csrfCookie: csrfToken, csrfHeader: csrfToken + "x", want: http.StatusForbidden},
		{name: "not bound to session", csrfCookie: "different-token", csrfHeader: "different-token", want: http.StatusForbidden},
		{name: "valid", csrfCookie: csrfToken, csrfHeader: csrfToken, want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, changePasswordPath, nil)
			request.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
			if test.csrfCookie != "" {
				request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: test.csrfCookie})
			}
			if test.csrfHeader != "" {
				request.Header.Set(CSRFHeaderName, test.csrfHeader)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	request = httptest.NewRequest(http.MethodPost, changePasswordPath, nil)
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("Bearer request was incorrectly subject to CSRF: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, userStatusPath, nil)
	request.Header.Set("Authorization", "Bearer invalid")
	request.AddCookie(&http.Cookie{Name: AdminSessionCookieName, Value: sessionToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid Authorization header fell back to cookie: %d", response.Code)
	}
}

func TestSessionCookieAttributesAndTrustedHTTPSDetection(t *testing.T) {
	t.Cleanup(func() {
		if err := ConfigureSessionCookies(conf.Default().Server); err != nil {
			t.Fatalf("restore cookie settings: %v", err)
		}
	})

	tests := []struct {
		name       string
		config     conf.Server
		remoteAddr string
		proto      string
		directTLS  bool
		wantSecure bool
	}{
		{name: "plain direct", config: conf.Default().Server, remoteAddr: "192.0.2.2:1234", proto: "https", wantSecure: false},
		{name: "direct TLS", config: conf.Default().Server, remoteAddr: "192.0.2.2:1234", directTLS: true, wantSecure: true},
		{name: "untrusted forwarded proto", config: conf.Server{SessionCookieSecure: "auto", TrustedProxies: []string{"10.0.0.0/8"}}, remoteAddr: "192.0.2.2:1234", proto: "https", wantSecure: false},
		{name: "trusted forwarded proto", config: conf.Server{SessionCookieSecure: "auto", TrustedProxies: []string{"10.0.0.0/8"}}, remoteAddr: "10.1.2.3:1234", proto: "https", wantSecure: true},
		{name: "trusted forwarded HTTP", config: conf.Server{SessionCookieSecure: "auto", TrustedProxies: []string{"10.0.0.0/8"}}, remoteAddr: "10.1.2.3:1234", proto: "http", wantSecure: false},
		{name: "always", config: conf.Server{SessionCookieSecure: "always"}, remoteAddr: "192.0.2.2:1234", wantSecure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ConfigureSessionCookies(test.config); err != nil {
				t.Fatalf("ConfigureSessionCookies() error = %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "http://octopus.test/login", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Forwarded-Proto", test.proto)
			if test.directTLS {
				request.TLS = &tls.ConnectionState{}
			}
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = request
			SetBrowserSessionCookies(context, "session", "csrf", time.Now().Add(time.Hour))
			cookies := response.Result().Cookies()
			if len(cookies) != 2 {
				t.Fatalf("Set-Cookie count = %d, want 2", len(cookies))
			}
			for _, cookie := range cookies {
				if cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != test.wantSecure {
					t.Errorf("cookie %s attributes = %#v", cookie.Name, cookie)
				}
			}
			if !cookies[0].HttpOnly || cookies[1].HttpOnly {
				t.Fatalf("session/CSRF HttpOnly attributes = %v/%v", cookies[0].HttpOnly, cookies[1].HttpOnly)
			}
		})
	}
}
