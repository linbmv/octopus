package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

// 登录请求体的 expire 由客户端提供，必须在服务端收敛：
// 0 → 默认值（配置）；-1 → 最大值（配置）；其余负值非法，回落默认；
// 正数按分钟签发但 clamp 到最大值，防止已认证者自授超长凭据。
// ExpiresAt 必须恒非 nil：曾因 <-1 不设过期导致对 nil NumericDate 调 Format panic。

func tokenLifetime(expiresMin int) time.Duration {
	defaultLifetime := time.Duration(conf.AppConfig.JWT.DefaultExpiryMinutes) * time.Minute
	maxLifetime := time.Duration(conf.AppConfig.JWT.MaxExpiryDays) * 24 * time.Hour

	switch {
	case expiresMin == -1:
		return maxLifetime
	case expiresMin > 0:
		lifetime := time.Duration(expiresMin) * time.Minute
		if lifetime > maxLifetime {
			return maxLifetime
		}
		return lifetime
	default:
		return defaultLifetime
	}
}

func GenerateJWTToken(expiresMin int) (string, string, error) {
	now := time.Now()
	user := op.UserGet()
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime(expiresMin))),
		Issuer:    conf.APP_NAME,
		// 将 token_version 编入 Subject 字段，验证时检查版本匹配
		Subject: fmt.Sprintf("v%d", user.TokenVersion),
	}
	// 使用独立的高熵 JWT 密钥签名，不再从 username+password 派生
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(user.JWTSecret))
	if err != nil {
		return "", "", err
	}
	return token, claims.ExpiresAt.Format(time.RFC3339), nil
}

func VerifyJWTToken(token string) bool {
	user := op.UserGet()
	claims := &jwt.RegisteredClaims{}
	jwtToken, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (interface{}, error) {
		// 使用独立的 JWT 密钥验证
		return []byte(user.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !jwtToken.Valid {
		return false
	}
	// 检查 token 版本：Subject 格式为 "v{version}"，不匹配则拒绝
	expectedSubject := fmt.Sprintf("v%d", user.TokenVersion)
	return claims.Subject == expectedSubject
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
