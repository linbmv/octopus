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
	jwtToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		// 使用独立的 JWT 密钥验证
		return []byte(user.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !jwtToken.Valid {
		return false
	}
	// 检查 token 版本：Subject 格式为 "v{version}"，不匹配则拒绝
	claims, ok := jwtToken.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	subject, _ := claims["sub"].(string)
	expectedSubject := fmt.Sprintf("v%d", user.TokenVersion)
	if subject != expectedSubject {
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
