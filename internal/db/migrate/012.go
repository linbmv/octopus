package migrate

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 12,
		Up:      requirePasswordChangeForLegacyDefaultAdmin,
	})
}

// requirePasswordChangeForLegacyDefaultAdmin upgrades only the historical
// admin/admin account. Comparing through bcrypt is intentional: plaintext or
// malformed values must never be mistaken for the legacy default credential.
func requirePasswordChangeForLegacyDefaultAdmin(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("users") {
		return nil
	}
	if !db.Migrator().HasColumn("users", "must_change_password") {
		return fmt.Errorf("users.must_change_password is missing after auto migration")
	}

	type adminRow struct {
		ID       uint   `gorm:"column:id"`
		Username string `gorm:"column:username"`
		Password string `gorm:"column:password"`
	}
	var admins []adminRow
	if err := db.Raw(
		"SELECT id, username, password FROM users WHERE username = ?",
		"admin",
	).Scan(&admins).Error; err != nil {
		return fmt.Errorf("query legacy admin account: %w", err)
	}

	for _, admin := range admins {
		// MySQL commonly uses a case-insensitive collation, so the SQL predicate
		// can also return "Admin" or "ADMIN". The historical default account is
		// exactly lowercase "admin"; enforce that invariant in Go as well.
		if admin.Username != "admin" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte("admin")); err != nil {
			// ErrMismatchedHashAndPassword and malformed/non-bcrypt values are both
			// non-matches. Neither is a migration failure or a reason to set the flag.
			continue
		}
		// Include the password hash in the predicate so a concurrent password
		// change cannot be followed by incorrectly restoring the forced-change flag.
		result := db.Model(&struct{ ID uint }{}).
			Table("users").
			Where("id = ? AND username = ? AND password = ?", admin.ID, "admin", admin.Password).
			Update("must_change_password", true)
		if result.Error != nil {
			return fmt.Errorf("mark legacy admin %d for password change: %w", admin.ID, result.Error)
		}
	}
	return nil
}
