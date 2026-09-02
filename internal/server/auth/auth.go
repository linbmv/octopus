package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// tokenVersionClaim 把签发时的会话版本写进 token。改密或改用户名会递增
	// 持久化的版本号，使所有更早签发的 token 立即失效。
	tokenVersionClaim = "tv"
	// jwtSecretBytes 是签名密钥的熵；密钥独立随机生成，与用户数据无关。
	jwtSecretBytes       = 32
	defaultTokenLifetime = 15 * time.Minute
	maxTokenLifetime     = 30 * 24 * time.Hour
)

var (
	secretMu     sync.RWMutex
	cachedSecret []byte
)

// EnsureSigningKey 加载持久化的签名密钥，缺失或不合法时生成并写入。
// 必须在开始处理请求前调用；失败即启动失败，不允许回落到弱密钥。
func EnsureSigningKey() error {
	if existing, err := op.SettingGetString(model.SettingKeyJWTSecret); err == nil && existing != "" {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(existing); decodeErr == nil && len(decoded) >= jwtSecretBytes {
			setSecret(decoded)
			return nil
		}
	}
	buf := make([]byte, jwtSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("failed to generate jwt secret: %w", err)
	}
	if err := op.SettingSetSecret(model.SettingKeyJWTSecret, base64.StdEncoding.EncodeToString(buf)); err != nil {
		return err
	}
	setSecret(buf)
	return nil
}

func setSecret(secret []byte) {
	secretMu.Lock()
	cachedSecret = secret
	secretMu.Unlock()
}

func signingKey() ([]byte, error) {
	secretMu.RLock()
	secret := cachedSecret
	secretMu.RUnlock()
	if len(secret) == 0 {
		return nil, fmt.Errorf("jwt signing key is not initialized")
	}
	return secret, nil
}

// tokenLifetime 收敛客户端传入的时长：0 取默认，-1 取上限，
// 正数 clamp 到上限，其余负值回落默认。
func tokenLifetime(expiresSec int) time.Duration {
	switch {
	case expiresSec == -1:
		return maxTokenLifetime
	case expiresSec > 0:
		if lifetime := time.Duration(expiresSec) * time.Second; lifetime <= maxTokenLifetime {
			return lifetime
		}
		return maxTokenLifetime
	default:
		return defaultTokenLifetime
	}
}

func GenerateJWTToken(expiresSec int) (string, int, error) {
	secret, err := signingKey()
	if err != nil {
		return "", 0, err
	}
	lifetime := tokenLifetime(expiresSec)
	now := time.Now()
	claims := jwt.MapClaims{
		"iat":             jwt.NewNumericDate(now),
		"nbf":             jwt.NewNumericDate(now),
		"iss":             conf.APP_NAME,
		"exp":             jwt.NewNumericDate(now.Add(lifetime)),
		tokenVersionClaim: op.TokenVersion(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		return "", 0, err
	}
	return token, int(lifetime.Seconds()), nil
}

func VerifyJWTToken(token string) bool {
	secret, err := signingKey()
	if err != nil {
		return false
	}
	parsed, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) {
		return secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(conf.APP_NAME),
	)
	if err != nil || !parsed.Valid {
		return false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	version, ok := tokenVersionOf(claims)
	if !ok {
		return false
	}
	return version == op.TokenVersion()
}

// tokenVersionOf 读取会话版本。缺少该声明的 token 属于旧格式，
// 一律判为无效，避免派生密钥时代签发的凭据在升级后继续可用。
func tokenVersionOf(claims jwt.MapClaims) (int, bool) {
	switch value := claims[tokenVersionClaim].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case string:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
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
