package auth

import (
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// 测试直接注入密钥，避免依赖数据库与设置表。
func withSecret(t *testing.T, secret []byte) {
	t.Helper()
	previous := cachedSecret
	setSecret(secret)
	t.Cleanup(func() { setSecret(previous) })
}

func TestSigningKeyIsNotDerivedFromUserData(t *testing.T) {
	withSecret(t, []byte("0123456789abcdef0123456789abcdef"))
	token, _, err := GenerateJWTToken(60)
	if err != nil {
		t.Fatalf("GenerateJWTToken: %v", err)
	}
	// 派生密钥时代的攻击者用 用户名+密码哈希 即可重签；换成独立密钥后必须失败。
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":             conf.APP_NAME,
		"exp":             jwt.NewNumericDate(time.Now().Add(time.Hour)),
		tokenVersionClaim: 1,
	})
	signed, err := forged.SignedString([]byte("admin" + "$2a$10$fakebcrypthash"))
	if err != nil {
		t.Fatalf("sign forged token: %v", err)
	}
	if VerifyJWTToken(signed) {
		t.Error("token signed with user-derived secret must not verify")
	}
	if !VerifyJWTToken(token) {
		t.Error("token signed with the real key must verify")
	}
}

func TestVerifyRejectsAlgNoneAndHS256Confusion(t *testing.T) {
	withSecret(t, []byte("0123456789abcdef0123456789abcdef"))
	claims := jwt.MapClaims{
		"iss":             conf.APP_NAME,
		"exp":             jwt.NewNumericDate(time.Now().Add(time.Hour)),
		tokenVersionClaim: 1,
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign alg=none token: %v", err)
	}
	if VerifyJWTToken(unsigned) {
		t.Error("alg=none token must be rejected by WithValidMethods")
	}
}

func TestVerifyRejectsTokenWithoutVersionClaim(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	withSecret(t, secret)
	// 旧格式 token 没有 tv 声明，升级后必须一律失效。
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": conf.APP_NAME,
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	if VerifyJWTToken(legacy) {
		t.Error("token without version claim must be rejected")
	}
}

func TestTokenLifetimeClamping(t *testing.T) {
	cases := []struct {
		name  string
		input int
		want  time.Duration
	}{
		{name: "zero uses default", input: 0, want: defaultTokenLifetime},
		{name: "negative one uses max", input: -1, want: maxTokenLifetime},
		{name: "other negative uses default", input: -99, want: defaultTokenLifetime},
		{name: "in range preserved", input: 3600, want: time.Hour},
		{name: "above max clamped", input: 999 * 24 * 3600, want: maxTokenLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenLifetime(tc.input); got != tc.want {
				t.Errorf("tokenLifetime(%d) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestEnsureSigningKeyRejectsShortStoredKey(t *testing.T) {
	// 短密钥应被判为不合法并重新生成，而不是原样使用。
	short := base64.StdEncoding.EncodeToString([]byte("tooshort"))
	if decoded, err := base64.StdEncoding.DecodeString(short); err != nil || len(decoded) >= jwtSecretBytes {
		t.Fatalf("fixture should decode to a short key, got %d bytes", len(decoded))
	}
}

func TestSetAuthCookieHardening(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		tls        bool
		forwarded  string
		wantSecure bool
	}{
		{name: "plain http", tls: false, wantSecure: false},
		{name: "direct tls", tls: true, wantSecure: true},
		{name: "proxied https", forwarded: "https", wantSecure: true},
		{name: "proxy chain first hop", forwarded: "https, http", wantSecure: true},
		{name: "proxied http", forwarded: "http", wantSecure: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", nil)
			if tc.tls {
				c.Request.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			SetAuthCookie(c, "token-value", 900)

			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("expected one cookie, got %d", len(cookies))
			}
			got := cookies[0]
			if got.Name != CookieName || got.Value != "token-value" {
				t.Errorf("cookie = %s=%s", got.Name, got.Value)
			}
			if !got.HttpOnly {
				t.Error("auth cookie must be HttpOnly")
			}
			if got.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", got.Secure, tc.wantSecure)
			}
			if got.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", got.SameSite)
			}
		})
	}
}

func TestClearAuthCookieMatchesAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/setting/list", nil)
	ClearAuthCookie(c)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	got := cookies[0]
	if got.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative", got.MaxAge)
	}
	if !got.HttpOnly {
		t.Error("cleared cookie must keep HttpOnly so the browser overwrites it")
	}
}
