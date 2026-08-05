package db

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/db/migrate"
	"github.com/bestruirui/octopus/internal/model"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMergeSQLiteDSNQueryPreservesExplicitValues(t *testing.T) {
	got, err := mergeDSNQuery("file:test.db?cache=shared&_busy_timeout=99", []string{
		"_busy_timeout=5000",
		"_journal_mode=WAL",
		"_txlock=immediate",
	})
	if err != nil {
		t.Fatalf("mergeDSNQuery() error = %v", err)
	}
	if strings.Count(got, "?") != 1 {
		t.Fatalf("merged DSN = %q, want one query delimiter", got)
	}
	_, rawQuery, _ := strings.Cut(got, "?")
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("ParseQuery() error = %v", err)
	}
	if values.Get("cache") != "shared" || values.Get("_busy_timeout") != "99" || values.Get("_journal_mode") != "WAL" || values.Get("_txlock") != "immediate" {
		t.Fatalf("merged query = %#v", values)
	}
}

func TestNormalizeMySQLDSNMergesRequiredOptions(t *testing.T) {
	got, err := normalizeMySQLDSN("user:pass@tcp(localhost:3306)/octopus?charset=latin1&loc=UTC&parseTime=false")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN() error = %v", err)
	}
	cfg, err := mysqldriver.ParseDSN(got)
	if err != nil {
		t.Fatalf("ParseDSN() error = %v", err)
	}
	if !cfg.ParseTime {
		t.Fatal("parseTime was not enforced")
	}
	if cfg.Loc.String() != time.UTC.String() {
		t.Fatalf("explicit location = %s, want UTC", cfg.Loc)
	}
	if cfg.Params["charset"] != "latin1" {
		t.Fatalf("explicit charset = %q, want latin1", cfg.Params["charset"])
	}

	defaults, err := normalizeMySQLDSN("user:pass@tcp(localhost:3306)/octopus")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN(defaults) error = %v", err)
	}
	defaultCfg, err := mysqldriver.ParseDSN(defaults)
	if err != nil {
		t.Fatalf("ParseDSN(defaults) error = %v", err)
	}
	if !defaultCfg.ParseTime || defaultCfg.Loc.String() != time.Local.String() || defaultCfg.Params["charset"] != "utf8mb4" {
		t.Fatalf("normalized defaults = parseTime:%v loc:%s params:%v", defaultCfg.ParseTime, defaultCfg.Loc, defaultCfg.Params)
	}

	t.Run("question mark in password does not hide explicit location", func(t *testing.T) {
		got, err := normalizeMySQLDSN("user:p?ass@tcp(localhost:3306)/octopus?loc=UTC")
		if err != nil {
			t.Fatalf("normalizeMySQLDSN() error = %v", err)
		}
		cfg, err := mysqldriver.ParseDSN(got)
		if err != nil {
			t.Fatalf("ParseDSN() error = %v", err)
		}
		if cfg.Passwd != "p?ass" || cfg.Loc.String() != time.UTC.String() {
			t.Fatalf("normalized DSN = password:%q loc:%s, want password preserved and UTC", cfg.Passwd, cfg.Loc)
		}
	})

	t.Run("location-like password text is not a query parameter", func(t *testing.T) {
		got, err := normalizeMySQLDSN("user:p?loc=UTC@tcp(localhost:3306)/octopus")
		if err != nil {
			t.Fatalf("normalizeMySQLDSN() error = %v", err)
		}
		cfg, err := mysqldriver.ParseDSN(got)
		if err != nil {
			t.Fatalf("ParseDSN() error = %v", err)
		}
		if cfg.Passwd != "p?loc=UTC" || cfg.Loc.String() != time.Local.String() {
			t.Fatalf("normalized DSN = password:%q loc:%s, want password preserved and Local default", cfg.Passwd, cfg.Loc)
		}
	})
}

func TestInitDBWithSQLiteAppliesSchemaAndConfiguresPool(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "octopus.db")
	if err := InitDB("sqlite", databasePath, true); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	got := GetDB()
	if got == nil {
		t.Fatal("GetDB() returned nil")
	}
	if got != db {
		t.Fatal("GetDB() did not return the package singleton")
	}

	for _, table := range []interface{}{
		&model.User{},
		&model.WebAuthnCredential{},
		&model.Channel{},
		&model.CapabilityEvidence{},
		&model.ChannelBaseline{},
		&model.DiagnosticSession{},
		&model.DiagnosticAttempt{},
		&model.ChannelPatch{},
		&model.Group{},
		&migrate.MigrationRecord{},
	} {
		if !got.Migrator().HasTable(table) {
			t.Errorf("expected migrated table for %T", table)
		}
	}
	for _, column := range []string{
		"header_rules",
		"json_rewrite_rules",
		"first_token_timeout_exception_enabled",
		"first_token_timeout_exception_seconds",
	} {
		if !got.Migrator().HasColumn(&model.Channel{}, column) {
			t.Errorf("expected migrated channels.%s column", column)
		}
	}
	if !got.Migrator().HasColumn(&model.CapabilityEvidence{}, "error_level") {
		t.Error("expected migrated capability_evidence.error_level column")
	}
	for _, column := range []string{"request_shape", "scope_fingerprint", "expires_at"} {
		if !got.Migrator().HasColumn(&model.ChannelBaseline{}, column) {
			t.Errorf("expected migrated channel_baselines.%s column", column)
		}
	}
	for _, table := range []interface{}{&model.DiagnosticSession{}, &model.DiagnosticAttempt{}, &model.ChannelPatch{}} {
		if !got.Migrator().HasTable(table) {
			t.Errorf("expected migrated table for %T", table)
		}
	}

	sqlDB, err := got.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 100 {
		t.Fatalf("MaxOpenConnections = %d, want 100", stats.MaxOpenConnections)
	}
}

func TestInitDBRejectsUnsupportedDatabaseType(t *testing.T) {
	err := InitDB("oracle", "ignored", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported database type: oracle") {
		t.Fatalf("InitDB() error = %v, want unsupported database type", err)
	}
}

func TestInitDBReturnsSQLiteOpenError(t *testing.T) {
	err := InitDB("sqlite", "/proc/octopus-tests/database.db", false)
	if err == nil {
		t.Fatal("InitDB() expected an error for an unwritable SQLite path")
	}
}
