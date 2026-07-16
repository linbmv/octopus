package middleware

import (
	"crypto/subtle"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/gin-gonic/gin"
)

const (
	AdminSessionCookieName = "octopus_admin_session"
	CSRFCookieName         = "octopus_csrf"
	CSRFHeaderName         = "X-Octopus-CSRF"
)

type trustedProxyRange struct {
	ip      net.IP
	network *net.IPNet
}

type sessionCookieSettings struct {
	secureMode     string
	trustedProxies []trustedProxyRange
}

var configuredSessionCookies atomic.Value

func init() {
	configuredSessionCookies.Store(sessionCookieSettings{secureMode: "auto"})
}

// ConfigureSessionCookies snapshots the listener's validated proxy and cookie
// policy. Config reloads require a restart for these fields, so a reload cannot
// make the running middleware trust forwarding headers that Gin itself does not
// yet trust.
func ConfigureSessionCookies(config conf.Server) error {
	settings := sessionCookieSettings{secureMode: config.SessionCookieSecure}
	if settings.secureMode != "auto" && settings.secureMode != "always" {
		return fmt.Errorf("invalid session cookie secure mode %q", settings.secureMode)
	}
	settings.trustedProxies = make([]trustedProxyRange, 0, len(config.TrustedProxies))
	for _, value := range config.TrustedProxies {
		value = strings.TrimSpace(value)
		if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
			settings.trustedProxies = append(settings.trustedProxies, trustedProxyRange{ip: ip})
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return fmt.Errorf("parse trusted proxy %q: %w", value, err)
		}
		settings.trustedProxies = append(settings.trustedProxies, trustedProxyRange{network: network})
	}
	configuredSessionCookies.Store(settings)
	return nil
}

func SetBrowserSessionCookies(c *gin.Context, sessionToken, csrfToken string, expiresAt time.Time) {
	secure := requestUsesHTTPS(c.Request)
	maxAgeSeconds := int64(time.Until(expiresAt) / time.Second)
	if maxAgeSeconds < 1 {
		maxAgeSeconds = 1
	}
	if maxAgeSeconds > math.MaxInt32 {
		maxAgeSeconds = math.MaxInt32
	}
	maxAge := int(maxAgeSeconds)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func ClearBrowserSessionCookies(c *gin.Context) {
	secure := requestUsesHTTPS(c.Request)
	for _, cookie := range []http.Cookie{
		{
			Name:     AdminSessionCookieName,
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		},
		{
			Name:     CSRFCookieName,
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		},
	} {
		cookie.Value = ""
		cookie.Expires = time.Unix(1, 0).UTC()
		cookie.MaxAge = -1
		http.SetCookie(c.Writer, &cookie)
	}
}

func RevokeRequestBrowserSession(c *gin.Context) {
	if cookie, err := c.Cookie(AdminSessionCookieName); err == nil {
		auth.RevokeBrowserSession(cookie)
	}
	ClearBrowserSessionCookies(c)
}

func validateCookieCSRF(c *gin.Context, sessionToken string) (sessionValid, csrfValid bool) {
	cookieToken, err := c.Cookie(CSRFCookieName)
	headerToken := c.GetHeader(CSRFHeaderName)
	sessionValid, boundTokenValid := auth.VerifyBrowserSessionRequest(sessionToken, headerToken)
	if !sessionValid || err != nil || cookieToken == "" || headerToken == "" || len(headerToken) != len(cookieToken) {
		return sessionValid, false
	}
	doubleSubmitValid := subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookieToken)) == 1
	return true, doubleSubmitValid && boundTokenValid
}

func requestUsesHTTPS(request *http.Request) bool {
	settings := configuredSessionCookies.Load().(sessionCookieSettings)
	if settings.secureMode == "always" {
		return true
	}
	if request == nil {
		return false
	}
	if request.TLS != nil {
		return true
	}
	if !remoteAddressTrusted(request.RemoteAddr, settings.trustedProxies) {
		return false
	}
	forwardedProto := request.Header.Get("X-Forwarded-Proto")
	if before, _, ok := strings.Cut(forwardedProto, ","); ok {
		forwardedProto = before
	}
	return strings.EqualFold(strings.TrimSpace(forwardedProto), "https")
}

func remoteAddressTrusted(remoteAddress string, trusted []trustedProxyRange) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = strings.Trim(remoteAddress, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, candidate := range trusted {
		if candidate.ip != nil && candidate.ip.Equal(ip) {
			return true
		}
		if candidate.network != nil && candidate.network.Contains(ip) {
			return true
		}
	}
	return false
}

func requestMethodNeedsCSRF(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}
