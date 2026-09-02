package op

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

// 导出的备份一旦带上 jwt_secret，拿到备份文件的人就能伪造任意登录态。
func TestBackupExportsOmitSecretSettings(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "secret.db"), false); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.GetDB().Model(&model.Setting{}).
		Where("key = ?", model.SettingKeyJWTSecret).
		Update("value", "leaked-signing-key").Error; err != nil {
		t.Fatalf("seed jwt secret: %v", err)
	}

	ctx := context.Background()
	full, err := DBExportAll(ctx)
	if err != nil {
		t.Fatalf("DBExportAll: %v", err)
	}
	assertNoSecretSettings(t, "DBExportAll", full.Settings)

	config, err := DBExportConfig(ctx)
	if err != nil {
		t.Fatalf("DBExportConfig: %v", err)
	}
	assertNoSecretSettings(t, "DBExportConfig", config.Settings)
}

// 导入自带的 jwt_secret 会用攻击者已知的密钥替换本机密钥；
// 导入较低的 token_version 会让已撤销的旧 token 复活。
func TestConfigImportIgnoresSecretSettings(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "import.db"), false); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := SettingSetSecret(model.SettingKeyJWTSecret, "local-key"); err != nil {
		t.Fatalf("seed jwt secret: %v", err)
	}
	if err := SettingSetString(model.SettingKeyTokenVersion, "9"); err == nil {
		t.Fatal("SettingSetString must refuse secret keys")
	}
	if err := SettingSetSecret(model.SettingKeyTokenVersion, "9"); err != nil {
		t.Fatalf("seed token version: %v", err)
	}

	if _, err := DBImportIncremental(context.Background(), &model.DBDump{
		Version: dbDumpVersion,
		Scope:   model.ConfigDumpScope,
		Settings: []model.Setting{
			{Key: model.SettingKeyJWTSecret, Value: "attacker-key"},
			{Key: model.SettingKeyTokenVersion, Value: "1"},
			{Key: model.SettingKeyCORSAllowOrigins, Value: "https://ok.test"},
		},
	}); err != nil {
		t.Fatalf("DBImportIncremental: %v", err)
	}
	// 导入接口在写库后会重建缓存，这里复现同一路径，
	// 顺带确认刷新不会把持久化的密钥挤出缓存。
	if err := InitCache(); err != nil {
		t.Fatalf("InitCache after import: %v", err)
	}

	after, err := SettingGetString(model.SettingKeyJWTSecret)
	if err != nil {
		t.Fatalf("read jwt secret after import: %v", err)
	}
	if after != "local-key" {
		t.Errorf("import must not replace the local signing key, got %q", after)
	}
	version, err := SettingGetString(model.SettingKeyTokenVersion)
	if err != nil {
		t.Fatalf("read token version: %v", err)
	}
	if version != "9" {
		t.Errorf("token version = %q, want 9 (import must not roll it back)", version)
	}
	origins, err := SettingGetString(model.SettingKeyCORSAllowOrigins)
	if err != nil {
		t.Fatalf("read cors origins: %v", err)
	}
	if origins != "https://ok.test" {
		t.Errorf("non-secret setting should still import, got %q", origins)
	}
}

func assertNoSecretSettings(t *testing.T, source string, settings []model.Setting) {
	t.Helper()
	for _, setting := range settings {
		if setting.Key.IsSecret() {
			t.Errorf("%s exported secret setting %q", source, setting.Key)
		}
	}
}
