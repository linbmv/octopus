package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateAPIKey(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey() error = %v", err)
	}
	prefix := "sk-" + conf.APP_NAME + "-"
	if !strings.HasPrefix(key, prefix) || len(key) != len(prefix)+48 {
		t.Fatalf("GenerateAPIKey() produced an invalid key shape")
	}
}

func TestMain(m *testing.M) {
	// 设置测试用的 JWT 配置默认值
	config := conf.Default()
	config.JWT.DefaultExpiryMinutes = 60
	config.JWT.MaxExpiryDays = 30
	if err := conf.Set(config); err != nil {
		panic(err)
	}

	if err := db.InitDB("sqlite", ":memory:", false); err != nil {
		panic(err)
	}

	const initialPasswordEnv = "OCTOPUS_INITIAL_ADMIN_PASSWORD"
	oldInitialPassword, hadInitialPassword := os.LookupEnv(initialPasswordEnv)
	if err := os.Setenv(initialPasswordEnv, "auth-package-test-password"); err != nil {
		panic(err)
	}
	if err := op.UserInit(); err != nil {
		panic(err)
	}
	if hadInitialPassword {
		if err := os.Setenv(initialPasswordEnv, oldInitialPassword); err != nil {
			panic(err)
		}
	} else {
		if err := os.Unsetenv(initialPasswordEnv); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	if err := db.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close auth test database: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func TestGenerateJWTTokenAlwaysSetsBoundedExpiry(t *testing.T) {
	config := conf.Current()
	defaultLifetime := time.Duration(config.JWT.DefaultExpiryMinutes) * time.Minute
	maxLifetime := time.Duration(config.JWT.MaxExpiryDays) * 24 * time.Hour

	cases := []struct {
		name       string
		expiresMin int
		want       time.Duration
		wantErr    error
	}{
		{"default 0", 0, defaultLifetime, nil},
		{"remember me -1", -1, maxLifetime, nil},
		{"invalid negative is rejected", -5, 0, ErrInvalidTokenExpiry},
		{"custom minutes", 60, time.Hour, nil},
		{"legacy frontend 86400 is safely clamped", 86_400, maxLifetime, nil},
		{"MaxInt is clamped before duration conversion", int(^uint(0) >> 1), maxLifetime, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, _, err := GenerateJWTToken(tc.expiresMin)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("GenerateJWTToken(%d) error = %v, want %v", tc.expiresMin, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateJWTToken(%d) error: %v", tc.expiresMin, err)
			}
			if token == "" {
				t.Fatal("token 为空")
			}

			// 解析 token 获取实际的 IssuedAt 和 ExpiresAt（跳过过期验证）
			user := op.UserGet()
			claims := &jwt.RegisteredClaims{}
			_, err = jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(user.JWTSecret), nil
			}, jwt.WithoutClaimsValidation())
			if err != nil {
				t.Fatalf("parse token: %v", err)
			}
			if claims.IssuedAt == nil || claims.ExpiresAt == nil {
				t.Fatalf("claims missing IssuedAt or ExpiresAt: %+v", claims)
			}

			lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
			if lifetime > tc.want+time.Minute || lifetime < tc.want-time.Minute {
				t.Fatalf("lifetime = %v, want ≈ %v", lifetime, tc.want)
			}
			if !VerifyJWTToken(token) {
				t.Fatal("签出的 token 必须能通过校验")
			}
		})
	}
}

func TestVerifyJWTTokenRejectsUnexpectedSigningMethod(t *testing.T) {
	claims := validTestClaims()
	user := op.UserGet()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(user.JWTSecret))
	if err != nil {
		t.Fatalf("sign HS384: %v", err)
	}
	if VerifyJWTToken(token) {
		t.Fatal("非 HS256 签名的 token 必须被 WithValidMethods 拒绝")
	}
}

func TestVerifyJWTTokenRequiresIssuerAndExpiration(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		claims jwt.RegisteredClaims
	}{
		{
			name: "missing issuer",
			claims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				Subject:   fmt.Sprintf("v%d", op.UserGet().TokenVersion),
			},
		},
		{
			name: "wrong issuer",
			claims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				Issuer:    "other-service",
				Subject:   fmt.Sprintf("v%d", op.UserGet().TokenVersion),
			},
		},
		{
			name: "missing expiration",
			claims: jwt.RegisteredClaims{
				Issuer:  conf.APP_NAME,
				Subject: fmt.Sprintf("v%d", op.UserGet().TokenVersion),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := signTestToken(t, jwt.SigningMethodHS256, test.claims)
			if VerifyJWTToken(token) {
				t.Fatal("VerifyJWTToken() accepted incomplete or incorrect registered claims")
			}
		})
	}
}

func validTestClaims() jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		Issuer:    conf.APP_NAME,
		Subject:   fmt.Sprintf("v%d", op.UserGet().TokenVersion),
	}
}

func signTestToken(t *testing.T, method jwt.SigningMethod, claims jwt.RegisteredClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(op.UserGet().JWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}
