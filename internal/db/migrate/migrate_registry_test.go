package migrate

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMigrationRegistriesRemainAvailableForIndependentDatabases(t *testing.T) {
	originalBefore := beforeAutoMigrations
	originalAfter := afterAutoMigrations
	t.Cleanup(func() {
		beforeAutoMigrations = originalBefore
		afterAutoMigrations = originalAfter
	})

	beforeRuns := 0
	afterRuns := 0
	beforeAutoMigrations = []Migration{{
		Version: 101,
		Up: func(*gorm.DB) error {
			beforeRuns++
			return nil
		},
	}}
	afterAutoMigrations = []Migration{{
		Version: 102,
		Up: func(*gorm.DB) error {
			afterRuns++
			return nil
		},
	}}

	for i := 0; i < 2; i++ {
		database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatalf("open independent database %d: %v", i, err)
		}
		if err := BeforeAutoMigrate(database); err != nil {
			t.Fatalf("run before-auto migrations for database %d: %v", i, err)
		}
		if err := AfterAutoMigrate(database); err != nil {
			t.Fatalf("run after-auto migrations for database %d: %v", i, err)
		}
	}

	if beforeRuns != 2 || afterRuns != 2 {
		t.Fatalf("migration runs = before:%d after:%d, want 2 for each independent database", beforeRuns, afterRuns)
	}
}
