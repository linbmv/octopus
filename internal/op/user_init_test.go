package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

// TestUserInitCreatesAdminWithRandomPasswordAndFlag 验证 P0-6：
// 首次启动无用户时创建 admin，使用非默认（非 "admin"）随机密码，且置 MustChangePassword。
func TestUserInitCreatesAdminWithRandomPasswordAndFlag(t *testing.T) {
	initTestDB(t)

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	var stored model.User
	if err := db.GetDB().First(&stored).Error; err != nil {
		t.Fatalf("query created user: %v", err)
	}
	if stored.Username != "admin" {
		t.Fatalf("username = %q, want admin", stored.Username)
	}
	if !stored.MustChangePassword {
		t.Fatal("expected MustChangePassword=true on fresh admin")
	}
	// 默认弱口令 "admin" 必须不再被接受。
	if err := stored.ComparePassword("admin"); err == nil {
		t.Fatal("initial password must not be the literal 'admin'")
	}
}

// TestUserInitDoesNotRecreateExistingUser 验证已存在用户时不重复创建、保留原状态。
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

// TestUserInitReturnsErrorOnDBFailure 验证非 RecordNotFound 的数据库错误不会误建 admin。
func TestUserInitReturnsErrorOnDBFailure(t *testing.T) {
	initTestDB(t)

	// 删除 users 表制造真实数据库错误（表不存在），First 应返回非 RecordNotFound 错误。
	if err := db.GetDB().Migrator().DropTable(&model.User{}); err != nil {
		t.Fatalf("drop users table: %v", err)
	}

	svc := NewUserService()
	err := svc.Init(context.Background())
	if err == nil {
		t.Fatal("expected error when users table is broken, got nil")
	}
	// 不应因为把错误当成 RecordNotFound 而尝试创建 admin。
	if svc.Get().Username != "" {
		t.Fatalf("must not populate user on DB failure, got %q", svc.Get().Username)
	}
}

// TestChangePasswordClearsMustChangeFlag 验证首登强制改密流程闭环。
func TestChangePasswordClearsMustChangeFlag(t *testing.T) {
	initTestDB(t)

	svc := NewUserService()
	if err := svc.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 取回初始随机密码无法直接得到，改为直接以已知密码重建初始用户以驱动改密。
	seed := model.User{Username: "admin", Password: "initpass", MustChangePassword: true}
	if err := seed.HashPassword(); err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := db.GetDB().Where("username = ?", "admin").Delete(&model.User{}).Error; err != nil {
		t.Fatalf("reset admin: %v", err)
	}
	if err := db.GetDB().Create(&seed).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	svc.user = seed

	if !UserMustChangePasswordFor(svc) {
		t.Fatal("precondition: expected MustChangePassword=true")
	}
	if err := svc.ChangePassword(context.Background(), "initpass", "newstrongpass"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if UserMustChangePasswordFor(svc) {
		t.Fatal("MustChangePassword should be cleared after change")
	}

	// 持久化也应清除。
	var stored model.User
	if err := db.GetDB().Where("username = ?", "admin").First(&stored).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if stored.MustChangePassword {
		t.Fatal("persisted MustChangePassword should be false after change")
	}
}

// UserMustChangePasswordFor 是测试辅助，读取指定 service 的强制改密标记。
func UserMustChangePasswordFor(s *UserService) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user.MustChangePassword
}
