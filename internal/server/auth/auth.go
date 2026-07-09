package auth

import (
	"crypto/rand"
	"math/big"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

// 登录请求体的 expire 由客户端提供，必须在服务端收敛：
// 0 → 默认 15 分钟；-1 → 30 天"记住我"；其余负值非法，回落默认；
// 正数按分钟签发但 clamp 到 30 天，防止已认证者自授超长凭据。
// ExpiresAt 必须恒非 nil：曾因 <-1 不设过期导致对 nil NumericDate 调 Format panic。
const (
	defaultTokenLifetime = 15 * time.Minute
	maxTokenLifetime     = 30 * 24 * time.Hour
)

func tokenLifetime(expiresMin int) time.Duration {
	switch {
	case expiresMin == -1:
		return maxTokenLifetime
	case expiresMin > 0:
		lifetime := time.Duration(expiresMin) * time.Minute
		if lifetime > maxTokenLifetime {
			return maxTokenLifetime
		}
		return lifetime
	default:
		return defaultTokenLifetime
	}
}

func GenerateJWTToken(expiresMin int) (string, string, error) {
	now := time.Now()
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime(expiresMin))),
		Issuer:    conf.APP_NAME,
	}
	user := op.UserGet()
	secret := user.Username + user.Password
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", "", err
	}
	return token, claims.ExpiresAt.Format(time.RFC3339), nil
}

func VerifyJWTToken(token string) bool {
	jwtToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		user := op.UserGet()
		secret := user.Username + user.Password
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !jwtToken.Valid {
		return false
	}
	return true
}

func GenerateAPIKey() string {
	const keyChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 48)
	maxI := big.NewInt(int64(len(keyChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return ""
		}
		b[i] = keyChars[n.Int64()]
	}
	return "sk-" + conf.APP_NAME + "-" + string(b)
}
