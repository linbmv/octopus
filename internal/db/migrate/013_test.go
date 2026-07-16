package migrate

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestAddStableEntityUUIDsUpgradesExistingRows(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE channels (id integer PRIMARY KEY, name text)`,
		`CREATE TABLE channel_keys (id integer PRIMARY KEY, channel_id integer)`,
		`CREATE TABLE groups (id integer PRIMARY KEY, name text)`,
		`CREATE TABLE group_items (id integer PRIMARY KEY, group_id integer)`,
		`CREATE TABLE api_keys (id integer PRIMARY KEY, name text)`,
		`INSERT INTO channels (id, name) VALUES (1, 'one'), (2, 'two')`,
		`INSERT INTO channel_keys (id, channel_id) VALUES (3, 1)`,
		`INSERT INTO groups (id, name) VALUES (4, 'group')`,
		`INSERT INTO group_items (id, group_id) VALUES (5, 4)`,
		`INSERT INTO api_keys (id, name) VALUES (6, 'key')`,
	} {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
	if err := addStableEntityUUIDs(database); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if err := addStableEntityUUIDs(database); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	for _, table := range []string{"channels", "channel_keys", "groups", "group_items", "api_keys"} {
		var values []string
		if err := database.Table(table).Order("id ASC").Pluck("uuid", &values).Error; err != nil {
			t.Fatalf("read %s UUIDs: %v", table, err)
		}
		for _, value := range values {
			if _, err := uuid.Parse(value); err != nil {
				t.Fatalf("%s UUID %q is invalid: %v", table, value, err)
			}
		}
	}
	for _, entity := range []any{&model.Channel{}, &model.ChannelKey{}, &model.Group{}, &model.GroupItem{}, &model.APIKey{}} {
		if !database.Migrator().HasIndex(entity, "UUID") {
			t.Errorf("UUID index missing for %T", entity)
		}
	}
}
