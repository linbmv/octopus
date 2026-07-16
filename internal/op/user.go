package op

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sync"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm"
)

var userService = NewUserService()

const (
	MinPasswordBytes                    = 8
	MaxPasswordBytes                    = 72
	initialAdminPasswordEnv             = "OCTOPUS_INITIAL_ADMIN_PASSWORD"
	initialAdminPasswordFileEnv         = "OCTOPUS_INITIAL_ADMIN_PASSWORD_FILE"
	defaultInitialAdminPasswordFile     = "data/initial-admin-password"
	initialCredentialFilePermissions    = os.FileMode(0o600)
	initialCredentialFileForbiddenModes = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
)

var (
	ErrInvalidCredentials     = errors.New("invalid credentials")
	ErrInvalidCurrentPassword = errors.New("invalid current password")
	ErrInvalidPassword        = errors.New("invalid password")
	ErrPasswordUnchanged      = errors.New("new password must differ from current password")
	ErrUsernameUnchanged      = errors.New("new username must differ from current username")
)

type UserService struct {
	mu                sync.RWMutex
	user              model.User
	initialCredential *initialCredentialFile
}

// initialCredentialFile records the identity of a bootstrap credential file.
// ChangePassword checks the identity again before removal so a path replacement
// cannot make Octopus delete an unrelated file.
type initialCredentialFile struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

func NewUserService() *UserService {
	return &UserService{}
}

func UserInit() error {
	return userService.Init(context.Background())
}

func (s *UserService) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var user model.User
	err := db.GetDB().WithContext(ctx).First(&user).Error
	if err == nil {
		credential, err := adoptInitialCredentialForExistingUser(&user)
		if err != nil {
			return fmt.Errorf("inspect initial admin credential: %w", err)
		}
		s.user = user
		s.initialCredential = credential
		if credential != nil {
			log.Warnf("initial admin credential file %q will be removed after the required password change", credential.path)
		}
		return nil
	}
	// 仅在确实没有用户时创建初始管理员；其他数据库错误（锁、连接、schema）
	// 必须向上返回，不能当成“用户不存在”而误建 admin。
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query initial user: %w", err)
	}

	initialPassword, credential, fromEnvironment, err := resolveInitialAdminCredential()
	if err != nil {
		return fmt.Errorf("prepare initial admin credential: %w", err)
	}
	jwtSecret, err := generateJWTSecret()
	if err != nil {
		return fmt.Errorf("generate jwt secret: %w", err)
	}
	user = model.User{
		Username:           "admin",
		Password:           initialPassword,
		MustChangePassword: true,
		JWTSecret:          jwtSecret,
		TokenVersion:       0,
	}
	if err := user.HashPassword(); err != nil {
		return err
	}
	if err := db.GetDB().WithContext(ctx).Create(&user).Error; err != nil {
		return err
	}
	s.user = user
	s.initialCredential = credential
	if fromEnvironment {
		log.Warnf("initial admin created with the password supplied through %s; change it immediately", initialAdminPasswordEnv)
	} else {
		log.Warnf("initial admin created; retrieve its one-time password from %q and change it immediately", credential.path)
	}
	return nil
}

// resolveInitialAdminCredential resolves the bootstrap password before any user
// row is created. An explicitly supplied (even empty) environment value is
// validated and takes precedence over the credential file.
func resolveInitialAdminCredential() (password string, credential *initialCredentialFile, fromEnvironment bool, err error) {
	if password, ok := os.LookupEnv(initialAdminPasswordEnv); ok {
		if err := ValidateNewPassword(password); err != nil {
			return "", nil, true, fmt.Errorf("invalid %s value: %w", initialAdminPasswordEnv, err)
		}
		return password, nil, true, nil
	}

	path := initialAdminPasswordFilePath()
	password, info, err := readSecureInitialPasswordFile(path)
	if err == nil {
		return password, newInitialCredentialFile(path, info, password), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", nil, false, err
	}

	password, err = generateInitialPassword()
	if err != nil {
		return "", nil, false, fmt.Errorf("generate initial password: %w", err)
	}
	if err := ValidateNewPassword(password); err != nil {
		return "", nil, false, fmt.Errorf("generated initial password failed validation: %w", err)
	}
	info, err = createInitialPasswordFile(path, password)
	if err != nil {
		// Another process may have created the file between Lstat and O_EXCL.
		// Re-read it through the same strict checks instead of overwriting it.
		if errors.Is(err, os.ErrExist) {
			password, info, err = readSecureInitialPasswordFile(path)
		}
		if err != nil {
			return "", nil, false, err
		}
	}
	return password, newInitialCredentialFile(path, info, password), false, nil
}

func newInitialCredentialFile(path string, info os.FileInfo, password string) *initialCredentialFile {
	return &initialCredentialFile{
		path:   path,
		info:   info,
		digest: sha256.Sum256([]byte(password)),
	}
}

func initialAdminPasswordFilePath() string {
	if path := os.Getenv(initialAdminPasswordFileEnv); path != "" {
		return path
	}
	return defaultInitialAdminPasswordFile
}

// adoptInitialCredentialForExistingUser recovers the file created immediately
// before a previous process crashed during database bootstrap. A valid but
// non-matching file is left untouched and is never tracked for deletion.
func adoptInitialCredentialForExistingUser(user *model.User) (*initialCredentialFile, error) {
	if user.Username != "admin" || !user.MustChangePassword {
		return nil, nil
	}

	path := initialAdminPasswordFilePath()
	password, info, err := readSecureInitialPasswordFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := user.ComparePassword(password); err != nil {
		log.Warnf("initial admin credential file %q does not match the current account and will not be managed", path)
		return nil, nil
	}
	return newInitialCredentialFile(path, info, password), nil
}

func readSecureInitialPasswordFile(path string) (_ string, _ os.FileInfo, err error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if err := validateInitialCredentialFileInfo(before); err != nil {
		return "", nil, fmt.Errorf("unsafe initial admin credential file %q: %w", path, err)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("open initial admin credential file %q: %w", path, err)
	}
	defer func() {
		err = closeInitialPasswordFile(file, path, err)
	}()

	opened, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("stat opened initial admin credential file %q: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("recheck initial admin credential file %q: %w", path, err)
	}
	if err := validateInitialCredentialFileInfo(opened); err != nil {
		return "", nil, fmt.Errorf("unsafe opened initial admin credential file %q: %w", path, err)
	}
	if err := validateInitialCredentialFileInfo(after); err != nil {
		return "", nil, fmt.Errorf("unsafe rechecked initial admin credential file %q: %w", path, err)
	}
	if !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return "", nil, fmt.Errorf("initial admin credential file %q changed while it was opened", path)
	}

	data, err := io.ReadAll(io.LimitReader(file, int64(MaxPasswordBytes+1)))
	if err != nil {
		return "", nil, fmt.Errorf("read initial admin credential file %q: %w", path, err)
	}
	password := string(data)
	if err := ValidateNewPassword(password); err != nil {
		return "", nil, fmt.Errorf("invalid password in initial admin credential file %q: %w", path, err)
	}
	return password, opened, nil
}

func validateInitialCredentialFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return fmt.Errorf("must be a regular file")
	}
	if info.Mode().Perm() != initialCredentialFilePermissions {
		return fmt.Errorf("permissions are %04o, want 0600", info.Mode().Perm())
	}
	if info.Mode()&initialCredentialFileForbiddenModes != 0 {
		return fmt.Errorf("special permission bits are not allowed")
	}
	return nil
}

func createInitialPasswordFile(path, password string) (os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, initialCredentialFilePermissions)
	if err != nil {
		return nil, fmt.Errorf("create initial admin credential file %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, closeInitialPasswordFile(
			file,
			path,
			fmt.Errorf("stat new initial admin credential file %q: %w", path, err),
		)
	}
	fail := func(cause error) (os.FileInfo, error) {
		cause = closeInitialPasswordFile(file, path, cause)
		removeInitialPasswordFileIfSame(path, info)
		return nil, cause
	}

	// OpenFile's mode is subject to umask. Chmod makes the final contract exact
	// while never granting group or world access.
	if err := file.Chmod(initialCredentialFilePermissions); err != nil {
		return fail(fmt.Errorf("secure initial admin credential file %q: %w", path, err))
	}
	info, err = file.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat secured initial admin credential file %q: %w", path, err))
	}
	if err := validateInitialCredentialFileInfo(info); err != nil {
		return fail(fmt.Errorf("validate new initial admin credential file %q: %w", path, err))
	}
	if written, err := io.WriteString(file, password); err != nil {
		return fail(fmt.Errorf("write initial admin credential file %q: %w", path, err))
	} else if written != len(password) {
		return fail(fmt.Errorf("write initial admin credential file %q: wrote %d of %d bytes", path, written, len(password)))
	}
	if err := file.Sync(); err != nil {
		return fail(fmt.Errorf("sync initial admin credential file %q: %w", path, err))
	}
	if err := file.Close(); err != nil {
		removeInitialPasswordFileIfSame(path, info)
		return nil, fmt.Errorf("close initial admin credential file %q: %w", path, err)
	}

	current, err := os.Lstat(path)
	if err != nil {
		removeInitialPasswordFileIfSame(path, info)
		return nil, fmt.Errorf("recheck new initial admin credential file %q: %w", path, err)
	}
	if err := validateInitialCredentialFileInfo(current); err != nil {
		removeInitialPasswordFileIfSame(path, info)
		return nil, fmt.Errorf("validate created initial admin credential file %q: %w", path, err)
	}
	if !os.SameFile(info, current) {
		return nil, fmt.Errorf("initial admin credential file %q changed while it was created", path)
	}
	return current, nil
}

func closeInitialPasswordFile(file *os.File, path string, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close initial admin credential file %q: %w", path, err))
	}
	return cause
}

func removeInitialPasswordFileIfSame(path string, expected os.FileInfo) {
	current, err := os.Lstat(path)
	if err == nil && expected != nil && os.SameFile(expected, current) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warnf("failed to remove initial admin credential file %q: %v", path, err)
		}
	}
}

// generateInitialPassword 生成 24 字符的高熵随机密码，用于初始管理员。
func generateInitialPassword() (string, error) {
	const passwordChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const length = 24
	b := make([]byte, length)
	maxI := big.NewInt(int64(len(passwordChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return "", err
		}
		b[i] = passwordChars[n.Int64()]
	}
	return string(b), nil
}

// generateJWTSecret 生成 32 字节（256 位）的高熵随机 JWT 密钥。
func generateJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	// 使用 base64 编码存储，避免二进制数据存储问题
	return fmt.Sprintf("%x", b), nil
}

func UserChangePasswordContext(ctx context.Context, oldPassword, newPassword string) error {
	return userService.ChangePassword(ctx, oldPassword, newPassword)
}

func ValidateNewPassword(password string) error {
	length := len(password)
	if !utf8.ValidString(password) || length < MinPasswordBytes || length > MaxPasswordBytes {
		return fmt.Errorf("%w: password must contain between %d and %d UTF-8 bytes", ErrInvalidPassword, MinPasswordBytes, MaxPasswordBytes)
	}
	return nil
}

func (s *UserService) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.user.ComparePassword(oldPassword); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCurrentPassword, err)
	}
	if oldPassword == newPassword {
		return ErrPasswordUnchanged
	}

	next := s.user
	next.Password = newPassword
	if err := next.HashPassword(); err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	// 改密同时：清除强制改密标记 + 递增 token_version 使旧 token 失效。
	newVersion := s.user.TokenVersion + 1
	result := db.GetDB().WithContext(ctx).Model(&s.user).
		Updates(map[string]any{
			"password":             next.Password,
			"must_change_password": false,
			"token_version":        newVersion,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update password: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("failed to update password: expected one affected row, got %d", result.RowsAffected)
	}

	s.user.Password = next.Password
	s.user.MustChangePassword = false
	s.user.TokenVersion = newVersion
	if err := s.removeInitialCredentialFileLocked(); err != nil {
		log.Errorf("password changed, but the initial admin credential file could not be removed: %v", err)
		return fmt.Errorf("password changed, but failed to remove initial admin credential file: %w", err)
	}
	return nil
}

// removeInitialCredentialFileLocked removes only the exact file that was
// created or adopted during Init. The caller must hold s.mu.
func (s *UserService) removeInitialCredentialFileLocked() error {
	credential := s.initialCredential
	if credential == nil {
		return nil
	}

	current, err := os.Lstat(credential.path)
	if errors.Is(err, os.ErrNotExist) {
		s.initialCredential = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %q before removal: %w", credential.path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(credential.info, current) {
		return fmt.Errorf("refusing to remove %q because its file identity changed", credential.path)
	}
	// Some filesystems can immediately reuse an inode after unlink. FileInfo's
	// device/inode identity alone can therefore produce a false match for a
	// replacement created at the same path. Re-open through the same strict
	// no-symlink/0600 checks and compare a digest of the high-entropy bootstrap
	// credential before deleting. No plaintext credential is retained in memory.
	password, verifiedInfo, err := readSecureInitialPasswordFile(credential.path)
	if err != nil {
		return fmt.Errorf("verify %q before removal: %w", credential.path, err)
	}
	currentDigest := sha256.Sum256([]byte(password))
	if !os.SameFile(credential.info, verifiedInfo) || subtle.ConstantTimeCompare(credential.digest[:], currentDigest[:]) != 1 {
		return fmt.Errorf("refusing to remove %q because its file identity or content changed", credential.path)
	}
	if err := os.Remove(credential.path); err != nil {
		return fmt.Errorf("remove %q: %w", credential.path, err)
	}
	s.initialCredential = nil
	return nil
}

func UserChangeUsernameContext(ctx context.Context, newUsername string) error {
	return userService.ChangeUsername(ctx, newUsername)
}

func (s *UserService) ChangeUsername(ctx context.Context, newUsername string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.user.Username == newUsername {
		return ErrUsernameUnchanged
	}
	newVersion := s.user.TokenVersion + 1
	result := db.GetDB().WithContext(ctx).Model(&s.user).Updates(map[string]any{
		"username":      newUsername,
		"token_version": newVersion,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update username: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("failed to update username: expected one affected row, got %d", result.RowsAffected)
	}
	s.user.Username = newUsername
	s.user.TokenVersion = newVersion
	return nil
}

func UserVerify(username, password string) error {
	return userService.Verify(username, password)
}

func (s *UserService) Verify(username, password string) error {
	s.mu.RLock()
	user := s.user
	s.mu.RUnlock()

	if username != user.Username {
		return ErrInvalidCredentials
	}
	if err := user.ComparePassword(password); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

func UserGet() model.User {
	return userService.Get()
}

func (s *UserService) Get() model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user
}

// UserMustChangePassword 返回当前用户是否处于强制改密状态。
func UserMustChangePassword() bool {
	userService.mu.RLock()
	defer userService.mu.RUnlock()
	return userService.user.MustChangePassword
}
