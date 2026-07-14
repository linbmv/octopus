package auth

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

func TestMain(m *testing.M) {
	// 设置测试用的 JWT 配置默认值
	conf.AppConfig.JWT.DefaultExpiryMinutes = 60
	conf.AppConfig.JWT.MaxExpiryDays = 30

	if err := db.InitDB("sqlite", ":memory:", false); err != nil {
		panic(err)
	}

	if err := op.UserInit(); err != nil {
		panic(err)
	}
	m.Run()
}

func TestGenerateJWTTokenAlwaysSetsBoundedExpiry(t *testing.T) {
	defaultLifetime := time.Duration(conf.AppConfig.JWT.DefaultExpiryMinutes) * time.Minute
	maxLifetime := time.Duration(conf.AppConfig.JWT.MaxExpiryDays) * 24 * time.Hour

	cases := []struct {
		name       string
		expiresMin int
		want       time.Duration
	}{
		{"default 0", 0, defaultLifetime},
		{"remember me -1", -1, maxLifetime},
		{"invalid negative falls back to default", -5, defaultLifetime},
		{"custom minutes", 60, time.Hour},
		{"huge value clamped to max", 100_000_000, maxLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, _, err := GenerateJWTToken(tc.expiresMin)
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

			lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
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
	now := time.Now()
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
	user := op.UserGet()
	secret := user.Username + user.Password
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign HS384: %v", err)
	}
	if VerifyJWTToken(token) {
		t.Fatal("非 HS256 签名的 token 必须被 WithValidMethods 拒绝")
	}
}
