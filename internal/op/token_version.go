package op

import (
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/bestruirui/octopus/internal/model"
)

// tokenVersion 是当前会话版本的内存副本。签名密钥不再由用户数据派生，
// 因此改密/改用户名需要一个显式的失效开关，而不是依赖密钥随之变化。
var tokenVersion atomic.Int64

// TokenVersionInit 从设置表载入会话版本，缺失时以 1 起始。
func TokenVersionInit() error {
	stored, err := SettingGetString(model.SettingKeyTokenVersion)
	if err == nil {
		if parsed, convErr := strconv.Atoi(stored); convErr == nil && parsed > 0 {
			tokenVersion.Store(int64(parsed))
			return nil
		}
	}
	tokenVersion.Store(1)
	return SettingSetSecret(model.SettingKeyTokenVersion, "1")
}

// TokenVersion 返回当前会话版本，供签发与校验共用。
func TokenVersion() int {
	return int(tokenVersion.Load())
}

// TokenVersionBump 递增并持久化会话版本，使所有已签发 token 立即失效。
// 持久化失败时回滚内存值，避免重启后版本倒退让旧 token 复活。
func TokenVersionBump() error {
	next := tokenVersion.Add(1)
	if err := SettingSetSecret(model.SettingKeyTokenVersion, strconv.FormatInt(next, 10)); err != nil {
		tokenVersion.Store(next - 1)
		return fmt.Errorf("failed to persist token version: %w", err)
	}
	return nil
}
