package op

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

// 敏感项一旦能被设置接口读出或覆写，独立签名密钥的意义就没了，
// 因此这里直接锁住 SettingList / SettingSetString 的行为。
func TestSettingListOmitsSecrets(t *testing.T) {
	settingCache.Set(model.SettingKeyProxyURL, "http://proxy.example")
	settingCache.Set(model.SettingKeyJWTSecret, "c2VjcmV0LXZhbHVl")
	settingCache.Set(model.SettingKeyTokenVersion, "7")
	t.Cleanup(func() {
		settingCache.Del(model.SettingKeyJWTSecret, model.SettingKeyTokenVersion)
	})

	settings, err := SettingList(t.Context())
	if err != nil {
		t.Fatalf("SettingList: %v", err)
	}
	sawProxy := false
	for _, setting := range settings {
		if setting.Key.IsSecret() {
			t.Errorf("secret %s leaked through SettingList", setting.Key)
		}
		if setting.Key == model.SettingKeyProxyURL {
			sawProxy = true
		}
	}
	if !sawProxy {
		t.Error("non-secret settings must still be listed")
	}
}

func TestSettingSetStringRejectsSecrets(t *testing.T) {
	for _, key := range []model.SettingKey{model.SettingKeyJWTSecret, model.SettingKeyTokenVersion} {
		if err := SettingSetString(key, "attacker-controlled"); err == nil {
			t.Errorf("SettingSetString(%s) must be rejected", key)
		}
	}
}

func TestSettingSetSecretRejectsNonSecretKey(t *testing.T) {
	if err := SettingSetSecret(model.SettingKeyProxyURL, "http://proxy.example"); err == nil {
		t.Error("SettingSetSecret must refuse non-secret keys")
	}
}

func TestSecretKeysAreNotDefaultSettings(t *testing.T) {
	// 默认设置会被批量插入并进入配置备份，敏感项不能出现在其中。
	for _, setting := range model.DefaultSettings() {
		if setting.Key.IsSecret() {
			t.Errorf("secret %s must not be a default setting", setting.Key)
		}
	}
}
