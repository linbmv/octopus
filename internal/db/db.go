package db

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/db/migrate"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

func InitDB(dbType, dsn string, debug bool) error {
	var err error
	gormConfig := gorm.Config{Logger: logger.Discard}
	if debug {
		gormConfig.Logger = logger.Default.LogMode(logger.Info)
	}

	switch dbType {
	case "sqlite":
		db, err = initSQLite(dsn, &gormConfig)
	case "mysql":
		db, err = initMySQL(dsn, &gormConfig)
	case "postgres", "postgresql":
		db, err = initPostgres(dsn, &gormConfig)
	default:
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	if err := migrate.BeforeAutoMigrate(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.WebAuthnCredential{},
		&model.Channel{},
		&model.ChannelKey{},
		&model.CapabilityEvidence{},
		&model.Group{},
		&model.GroupItem{},
		&model.LLMInfo{},
		&model.APIKey{},
		&model.Setting{},
		&model.StatsTotal{},
		&model.StatsDaily{},
		&model.StatsHourly{},
		&model.StatsChannel{},
		&model.StatsChannelKey{},
		&model.StatsAPIKey{},
		&model.RelayLog{},
		&migrate.MigrationRecord{},
	); err != nil {
		return err
	}
	if err := migrate.AfterAutoMigrate(db); err != nil {
		return err
	}
	return nil
}

func initSQLite(path string, config *gorm.Config) (*gorm.DB, error) {
	params := []string{
		"_journal_mode=WAL",
		"_synchronous=NORMAL",
		"_cache_size=10000",
		"_busy_timeout=5000",
		// Delete and restore transactions read before writing. With SQLite's
		// deferred default, concurrent writers can both establish read snapshots
		// and then fail immediately with SQLITE_BUSY when upgrading, bypassing the
		// busy timeout. BEGIN IMMEDIATE acquires the write reservation up front so
		// writers wait at transaction start instead of leaving partial operations.
		"_txlock=immediate",
		"_foreign_keys=ON",
		"_auto_vacuum=INCREMENTAL",
		"_mmap_size=268435456",
		"_locking_mode=NORMAL",
	}
	dsn, err := mergeDSNQuery(path, params)
	if err != nil {
		return nil, fmt.Errorf("invalid SQLite DSN: %w", err)
	}
	return gorm.Open(sqlite.Open(dsn), config)
}

func initMySQL(dsn string, config *gorm.Config) (*gorm.DB, error) {
	// DSN 格式: user:password@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
	normalized, err := normalizeMySQLDSN(dsn)
	if err != nil {
		return nil, err
	}
	return gorm.Open(mysql.Open(normalized), config)
}

func mergeDSNQuery(dsn string, defaults []string) (string, error) {
	base, rawQuery, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	for _, item := range defaults {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return "", fmt.Errorf("invalid default query parameter %q", item)
		}
		if _, exists := values[key]; !exists {
			values.Set(key, value)
		}
	}
	return base + "?" + values.Encode(), nil
}

func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL DSN: %w", err)
	}
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	if _, ok := cfg.Params["charset"]; !ok {
		cfg.Params["charset"] = "utf8mb4"
	}
	// GORM models use time.Time, so parseTime is a required invariant rather
	// than an optional caller preference.
	cfg.ParseTime = true
	if !dsnHasQueryParameter(dsn, "loc") {
		cfg.Loc = time.Local
	}
	return cfg.FormatDSN(), nil
}

func dsnHasQueryParameter(dsn, key string) bool {
	// Match go-sql-driver/mysql's DSN grammar: passwords may contain any
	// character (including '?' and '/'), so parameters start at the first '?'
	// after the final '/' database separator, not the first '?' in the DSN.
	databaseSeparator := strings.LastIndexByte(dsn, '/')
	if databaseSeparator < 0 {
		return false
	}
	_, rawQuery, ok := strings.Cut(dsn[databaseSeparator+1:], "?")
	if !ok {
		return false
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	_, ok = values[key]
	return ok
}

func initPostgres(dsn string, config *gorm.Config) (*gorm.DB, error) {
	// DSN 格式: host=localhost user=postgres password=xxx dbname=octopus port=5432 sslmode=disable
	// AutoMigrate changes result shapes during startup. pgx's implicit prepared
	// statement cache can retain plans for those old shapes; clearing plans with
	// DEALLOCATE/DISCARD is connection-local and also leaves the client cache out
	// of sync with the server. Simple protocol avoids both failure modes.
	return gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), config)
}

func Close() error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func GetDB() *gorm.DB {
	return db
}
