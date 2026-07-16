package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidTokenExpiry       = errors.New("invalid token expiry")
	ErrInvalidTokenExpiryConfig = errors.New("invalid token expiry configuration")
)

const (
	minutesPerDay      int64 = 24 * 60
	maxDurationMinutes int64 = (1<<63 - 1) / int64(time.Minute)
)

// 登录请求体的 expiry 由客户端提供，必须在服务端收敛：
// 0 → 默认值（配置）；-1 → 最大值（配置）；其余负值非法并拒绝；
// 正数按分钟签发但 clamp 到最大值，防止已认证者自授超长凭据。
// ExpiresAt 必须恒非 nil：曾因 <-1 不设过期导致对 nil NumericDate 调 Format panic。

func tokenLifetime(expiresMin int) (time.Duration, error) {
	config := conf.Current()
	defaultMinutes := int64(config.JWT.DefaultExpiryMinutes)
	maxDays := int64(config.JWT.MaxExpiryDays)
	if defaultMinutes <= 0 || maxDays <= 0 || maxDays > maxDurationMinutes/minutesPerDay {
		return 0, ErrInvalidTokenExpiryConfig
	}
	maxMinutes := maxDays * minutesPerDay
	if defaultMinutes > maxMinutes || defaultMinutes > maxDurationMinutes {
		return 0, ErrInvalidTokenExpiryConfig
	}

	var lifetimeMinutes int64
	switch {
	case expiresMin == -1:
		lifetimeMinutes = maxMinutes
	case expiresMin > 0:
		lifetimeMinutes = int64(expiresMin)
		if lifetimeMinutes > maxMinutes {
			lifetimeMinutes = maxMinutes
		}
	case expiresMin == 0:
		lifetimeMinutes = defaultMinutes
	default:
		return 0, fmt.Errorf("%w: value must be -1, 0, or a positive number of minutes", ErrInvalidTokenExpiry)
	}

	// The value is clamped in integer space before converting to Duration, so
	// MaxInt and architecture-specific int widths cannot overflow multiplication.
	return time.Duration(lifetimeMinutes) * time.Minute, nil
}

func GenerateJWTToken(expiresMin int) (string, string, error) {
	now := time.Now()
	user := op.UserGet()
	lifetime, err := tokenLifetime(expiresMin)
	if err != nil {
		return "", "", err
	}
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(lifetime)),
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
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(conf.APP_NAME),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !jwtToken.Valid {
		return false
	}
	// 检查 token 版本：Subject 格式为 "v{version}"，不匹配则拒绝
	expectedSubject := fmt.Sprintf("v%d", user.TokenVersion)
	return claims.Subject == expectedSubject
}

func GenerateAPIKey() (string, error) {
	const keyChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 48)
	maxI := big.NewInt(int64(len(keyChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return "", fmt.Errorf("generate API key: %w", err)
		}
		b[i] = keyChars[n.Int64()]
	}
	return "sk-" + conf.APP_NAME + "-" + string(b), nil
}
