package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRequirePasswordChangeForLegacyDefaultAdmin(t *testing.T) {
	db := openLegacyAdminMigrationDB(t)
	admin := model.User{
		Username:           "admin",
		Password:           "admin",
		MustChangePassword: false,
		JWTSecret:          "migration-test-secret",
	}
	if err := admin.HashPassword(); err != nil {
		t.Fatalf("hash legacy password: %v", err)
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create legacy admin: %v", err)
	}

	if err := requirePasswordChangeForLegacyDefaultAdmin(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var migrated model.User
	if err := db.First(&migrated, admin.ID).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if !migrated.MustChangePassword {
		t.Fatal("bcrypt admin/admin account was not marked for password change")
	}

	// Re-running before a migration record is committed must be safe and stable.
	if err := requirePasswordChangeForLegacyDefaultAdmin(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if err := db.First(&migrated, admin.ID).Error; err != nil {
		t.Fatalf("reload admin after second migration: %v", err)
	}
	if !migrated.MustChangePassword {
		t.Fatal("idempotent migration cleared the password-change flag")
	}
}

func TestRequirePasswordChangeForLegacyDefaultAdminSkipsNonMatches(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		password     string
		hashPassword bool
	}{
		{name: "admin with another bcrypt password", username: "admin", password: "not-the-default", hashPassword: true},
		{name: "other username with bcrypt admin password", username: "operator", password: "admin", hashPassword: true},
		{name: "plaintext admin is not bcrypt", username: "admin", password: "admin", hashPassword: false},
		{name: "malformed bcrypt-like value", username: "admin", password: "$2a$broken", hashPassword: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openLegacyAdminMigrationDB(t)
			user := model.User{
				Username:           test.username,
				Password:           test.password,
				MustChangePassword: false,
				JWTSecret:          "migration-test-secret",
			}
			if test.hashPassword {
				if err := user.HashPassword(); err != nil {
					t.Fatalf("hash password: %v", err)
				}
			}
			if err := db.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
			}

			if err := requirePasswordChangeForLegacyDefaultAdmin(db); err != nil {
				t.Fatalf("migration: %v", err)
			}
			var migrated model.User
			if err := db.First(&migrated, user.ID).Error; err != nil {
				t.Fatalf("reload user: %v", err)
			}
			if migrated.MustChangePassword {
				t.Fatal("non-legacy account was incorrectly marked for password change")
			}
		})
	}
}

func TestRequirePasswordChangeForLegacyDefaultAdminSkipsCaseVariantWithInsensitiveCollation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id integer PRIMARY KEY AUTOINCREMENT,
		username text COLLATE NOCASE NOT NULL UNIQUE,
		password text NOT NULL,
		must_change_password numeric NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("create case-insensitive users table: %v", err)
	}

	user := model.User{Password: "admin"}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO users (username, password, must_change_password) VALUES (?, ?, ?)",
		"Admin", user.Password, false,
	).Error; err != nil {
		t.Fatalf("create case-variant admin: %v", err)
	}

	if err := requirePasswordChangeForLegacyDefaultAdmin(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var mustChangePassword bool
	if err := db.Raw("SELECT must_change_password FROM users WHERE username = ?", "Admin").Scan(&mustChangePassword).Error; err != nil {
		t.Fatalf("reload case-variant admin: %v", err)
	}
	if mustChangePassword {
		t.Fatal("case-variant username was incorrectly treated as the historical lowercase admin")
	}
}

func TestRequirePasswordChangeForLegacyDefaultAdminSkipsFreshDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := requirePasswordChangeForLegacyDefaultAdmin(db); err != nil {
		t.Fatalf("fresh database migration: %v", err)
	}
	if db.Migrator().HasTable("users") {
		t.Fatal("migration unexpectedly created users table")
	}
}

func openLegacyAdminMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("auto migrate user: %v", err)
	}
	return db
}
