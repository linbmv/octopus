package auth

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateJWTTokenAlwaysSetsBoundedExpiry(t *testing.T) {
	cases := []struct {
		name       string
		expiresMin int
		want       time.Duration
	}{
		{"default 0", 0, defaultTokenLifetime},
		{"remember me -1", -1, maxTokenLifetime},
		{"invalid negative falls back to default", -5, defaultTokenLifetime},
		{"custom minutes", 60, time.Hour},
		{"huge value clamped to max", 100_000_000, maxTokenLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, expireStr, err := GenerateJWTToken(tc.expiresMin)
			if err != nil {
				t.Fatalf("GenerateJWTToken(%d) error: %v", tc.expiresMin, err)
			}
			if token == "" {
				t.Fatal("token 为空")
			}
			expireAt, err := time.Parse(time.RFC3339, expireStr)
			if err != nil {
				t.Fatalf("expire %q 不是 RFC3339: %v", expireStr, err)
			}
			lifetime := time.Until(expireAt)
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
