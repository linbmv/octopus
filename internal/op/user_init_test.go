package op

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	projectlog "github.com/bestruirui/octopus/internal/utils/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUserInitCreatesAdminWithRandomPasswordFile(t *testing.T) {
	initTestDB(t)
	credentialPath := configureRandomInitialCredential(t)

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	password := readCredentialPassword(t, credentialPath)
	if len(password) != 24 {
		t.Fatalf("generated password length = %d, want 24", len(password))
	}
	if err := ValidateNewPassword(password); err != nil {
		t.Fatalf("generated password validation: %v", err)
	}
	if err := svc.Verify("admin", password); err != nil {
		t.Fatalf("generated password cannot log in: %v", err)
	}
	if err := svc.Verify("admin", "admin"); err == nil {
		t.Fatal("initial password must not be the literal 'admin'")
	}

	info, err := os.Lstat(credentialPath)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("credential mode = %v, want regular file", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %04o, want 0600", info.Mode().Perm())
	}

	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		t.Fatalf("query created user: %v", err)
	}
	if stored.Username != "admin" || !stored.MustChangePassword {
		t.Fatalf("created user = %#v, want forced-password-change admin", stored)
	}
}

func TestUserInitPasswordFileWorksWhenWarningsAreDisabled(t *testing.T) {
	initTestDB(t)
	credentialPath := configureRandomInitialCredential(t)
	projectlog.SetLevel("error")
	t.Cleanup(func() { projectlog.SetLevel("info") })

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := svc.Verify("admin", readCredentialPassword(t, credentialPath)); err != nil {
		t.Fatalf("credential file password cannot log in at error log level: %v", err)
	}
}

func TestUserInitUsesEnvironmentPasswordWithoutWritingFileOrSecretToLogs(t *testing.T) {
	initTestDB(t)
	password := "Env-Initial-Secret-42"
	credentialPath := filepath.Join(t.TempDir(), "must-not-exist")
	t.Setenv(initialAdminPasswordEnv, password)
	t.Setenv(initialAdminPasswordFileEnv, credentialPath)

	core, observed := observer.New(zapcore.DebugLevel)
	originalLogger := projectlog.Logger
	projectlog.Logger = zap.New(core).Sugar()
	t.Cleanup(func() { projectlog.Logger = originalLogger })

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := svc.Verify("admin", password); err != nil {
		t.Fatalf("environment password cannot log in: %v", err)
	}
	if _, err := os.Lstat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential file should not be written when environment password is supplied, stat error=%v", err)
	}
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message, password) {
			t.Fatalf("initial password leaked to logs: %q", entry.Message)
		}
	}
}

func TestUserInitRejectsInvalidEnvironmentPasswordBeforeCreatingUser(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{name: "empty", password: ""},
		{name: "too short", password: "1234567"},
		{name: "too long", password: strings.Repeat("a", MaxPasswordBytes+1)},
		{name: "invalid UTF-8", password: "1234567\xff"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initTestDB(t)
			t.Setenv(initialAdminPasswordEnv, test.password)
			t.Setenv(initialAdminPasswordFileEnv, filepath.Join(t.TempDir(), "must-not-exist"))

			svc := NewUserService()
			if err := svc.Init(context.Background()); !errors.Is(err, ErrInvalidPassword) {
				t.Fatalf("Init error = %v, want ErrInvalidPassword", err)
			}
			assertNoUsers(t)
		})
	}
}

func TestUserInitRejectsUnsafeCredentialDeliveryBeforeCreatingUser(t *testing.T) {
	const validPassword = "safe-initial-password"
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name: "broad permissions",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "credential")
				writeCredentialFile(t, path, validPassword, 0o644)
				return path
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				target := filepath.Join(dir, "target")
				writeCredentialFile(t, target, validPassword, 0o600)
				path := filepath.Join(dir, "credential")
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
				return path
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "credential")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create directory credential: %v", err)
				}
				return path
			},
		},
		{
			name: "invalid password",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "credential")
				writeCredentialFile(t, path, "short", 0o600)
				return path
			},
		},
		{
			name: "unwritable destination",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing-parent", "credential")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initTestDB(t)
			credentialPath := test.setup(t)
			unsetEnvironmentForTest(t, initialAdminPasswordEnv)
			t.Setenv(initialAdminPasswordFileEnv, credentialPath)

			svc := NewUserService()
			if err := svc.Init(context.Background()); err == nil {
				t.Fatal("Init unexpectedly accepted unsafe credential delivery")
			}
			assertNoUsers(t)
		})
	}
}

func TestUserInitReusesSecureCredentialFileAfterPreDatabaseCrash(t *testing.T) {
	initTestDB(t)
	credentialPath := filepath.Join(t.TempDir(), "initial-admin-password")
	password := "recovered-initial-password"
	writeCredentialFile(t, credentialPath, password, 0o600)
	unsetEnvironmentForTest(t, initialAdminPasswordEnv)
	t.Setenv(initialAdminPasswordFileEnv, credentialPath)

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := svc.Verify("admin", password); err != nil {
		t.Fatalf("pre-existing secure credential was not reused: %v", err)
	}
	if got := readCredentialPassword(t, credentialPath); got != password {
		t.Fatalf("credential file was overwritten: got %q", got)
	}
}

func TestUserInitRestartAdoptsMatchingCredentialAndChangePasswordDeletesIt(t *testing.T) {
	initTestDB(t)
	credentialPath := configureRandomInitialCredential(t)

	first := NewUserService()
	if err := first.Init(context.Background()); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	password := readCredentialPassword(t, credentialPath)

	restarted := NewUserService()
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatalf("restart Init: %v", err)
	}
	if restarted.initialCredential == nil {
		t.Fatal("restart did not adopt the matching credential file")
	}
	if err := restarted.ChangePassword(context.Background(), password, "new-strong-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := os.Lstat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential file was not removed after password change, stat error=%v", err)
	}
	if err := restarted.Verify("admin", "new-strong-password"); err != nil {
		t.Fatalf("new password cannot log in: %v", err)
	}
}

func TestChangePasswordDatabaseFailureKeepsCredentialFile(t *testing.T) {
	initTestDB(t)
	credentialPath := configureRandomInitialCredential(t)

	first := NewUserService()
	if err := first.Init(context.Background()); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	password := readCredentialPassword(t, credentialPath)
	restarted := NewUserService()
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatalf("restart Init: %v", err)
	}
	if err := db.GetDB().Migrator().DropTable(&model.User{}); err != nil {
		t.Fatalf("drop users table: %v", err)
	}

	if err := restarted.ChangePassword(context.Background(), password, "new-strong-password"); err == nil {
		t.Fatal("ChangePassword unexpectedly succeeded with a broken database")
	}
	if got := readCredentialPassword(t, credentialPath); got != password {
		t.Fatalf("credential changed after database failure: got %q", got)
	}
}

func TestChangePasswordDoesNotDeleteReplacedCredentialPath(t *testing.T) {
	initTestDB(t)
	credentialPath := configureRandomInitialCredential(t)

	first := NewUserService()
	if err := first.Init(context.Background()); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	password := readCredentialPassword(t, credentialPath)
	restarted := NewUserService()
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatalf("restart Init: %v", err)
	}
	if err := os.Remove(credentialPath); err != nil {
		t.Fatalf("remove original credential: %v", err)
	}
	const replacement = "unrelated-replacement-file"
	writeCredentialFile(t, credentialPath, replacement, 0o600)

	err := restarted.ChangePassword(context.Background(), password, "new-strong-password")
	if err == nil || !strings.Contains(err.Error(), "password changed") {
		t.Fatalf("ChangePassword error = %v, want explicit partial-success cleanup error", err)
	}
	if got := readCredentialPassword(t, credentialPath); got != replacement {
		t.Fatalf("replacement path was deleted or changed: got %q", got)
	}
	if err := restarted.Verify("admin", "new-strong-password"); err != nil {
		t.Fatalf("database update should remain effective after guarded cleanup failure: %v", err)
	}
}

func TestUserInitDoesNotAdoptNonMatchingCredentialFile(t *testing.T) {
	initTestDB(t)
	credentialPath := filepath.Join(t.TempDir(), "initial-admin-password")
	writeCredentialFile(t, credentialPath, "different-valid-password", 0o600)
	unsetEnvironmentForTest(t, initialAdminPasswordEnv)
	t.Setenv(initialAdminPasswordFileEnv, credentialPath)

	user := model.User{
		Username:           "admin",
		Password:           "actual-initial-password",
		MustChangePassword: true,
		JWTSecret:          "test-jwt-secret",
	}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash user password: %v", err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if svc.initialCredential != nil {
		t.Fatal("non-matching credential file was adopted")
	}
	if err := svc.ChangePassword(context.Background(), "actual-initial-password", "new-strong-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if got := readCredentialPassword(t, credentialPath); got != "different-valid-password" {
		t.Fatalf("non-matching credential was modified: got %q", got)
	}
}

func TestUserInitDoesNotRecreateExistingUser(t *testing.T) {
	initTestDB(t)

	existing := model.User{Username: "operator", Password: "keepme", MustChangePassword: false}
	if err := existing.HashPassword(); err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := db.GetDB().Create(&existing).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	var count int64
	db.GetDB().Model(&model.User{}).Count(&count)
	if count != 1 {
		t.Fatalf("user count = %d, want 1 (must not recreate)", count)
	}
	if svc.Get().Username != "operator" {
		t.Fatalf("loaded username = %q, want operator", svc.Get().Username)
	}
}

func TestUserInitReturnsErrorOnDBFailure(t *testing.T) {
	initTestDB(t)

	if err := db.GetDB().Migrator().DropTable(&model.User{}); err != nil {
		t.Fatalf("drop users table: %v", err)
	}

	svc := NewUserService()
	err := svc.Init(context.Background())
	if err == nil {
		t.Fatal("expected error when users table is broken, got nil")
	}
	if svc.Get().Username != "" {
		t.Fatalf("must not populate user on DB failure, got %q", svc.Get().Username)
	}
}

func TestChangePasswordClearsMustChangeFlag(t *testing.T) {
	svc := newPersistedUserService(t, "initpass", true, 0)

	if !UserMustChangePasswordFor(svc) {
		t.Fatal("precondition: expected MustChangePassword=true")
	}
	if err := svc.ChangePassword(context.Background(), "initpass", "newstrongpass"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if UserMustChangePasswordFor(svc) {
		t.Fatal("MustChangePassword should be cleared after change")
	}

	var stored model.User
	if err := db.GetDB().Where("username = ?", "admin").First(&stored).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if stored.MustChangePassword {
		t.Fatal("persisted MustChangePassword should be false after change")
	}
	if svc.Get().TokenVersion != 1 || stored.TokenVersion != 1 {
		t.Fatalf("token version memory=%d stored=%d, want 1", svc.Get().TokenVersion, stored.TokenVersion)
	}
}

func configureRandomInitialCredential(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "initial-admin-password")
	unsetEnvironmentForTest(t, initialAdminPasswordEnv)
	t.Setenv(initialAdminPasswordFileEnv, path)
	return path
}

func unsetEnvironmentForTest(t *testing.T, key string) {
	t.Helper()
	oldValue, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, oldValue)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func writeCredentialFile(t *testing.T, path, password string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(password), mode); err != nil {
		t.Fatalf("write credential file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod credential file: %v", err)
	}
}

func readCredentialPassword(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	return string(data)
}

func assertNoUsers(t *testing.T) {
	t.Helper()
	var count int64
	if err := db.GetDB().Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("user count = %d, want 0", count)
	}
}

func UserMustChangePasswordFor(s *UserService) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user.MustChangePassword
}
