package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

// SchemaProfile describes the physical schema found before application migrations run.
// It intentionally relies on physical markers instead of migration_records: Edge and
// upstream reused several numeric migration versions for different operations.
type SchemaProfile string

const (
	SchemaProfileUnknown    SchemaProfile = "unknown"
	SchemaProfileUpstream   SchemaProfile = "upstream"
	SchemaProfileLegacyEdge SchemaProfile = "legacy-edge"
)

// DetectSchemaProfile identifies the legacy Edge layout that must not be passed to
// upstream's destructive single-key/channel-model migrations yet.
func DetectSchemaProfile(db *gorm.DB) SchemaProfile {
	if db == nil {
		return SchemaProfileUnknown
	}
	if !db.Migrator().HasTable("channels") {
		return SchemaProfileUnknown
	}
	// The additive C06/upstream schema intentionally contains both the legacy
	// single-value columns and the restored multi-value fields. An Edge v2
	// database has BaseUrls/Keys but no base_url/key columns, so check the
	// upstream pair first to make restarts idempotent.
	if db.Migrator().HasColumn("channels", "base_url") && db.Migrator().HasColumn("channels", "key") {
		return SchemaProfileUpstream
	}

	// These markers are deliberately physical names. Edge uses them for its
	// multi-key/multi-URL and channel-model configuration; the upstream pair
	// check above has already excluded the additive compatibility schema.
	for _, marker := range []struct {
		table  string
		column string
	}{
		{table: "channels", column: "base_urls"},
		{table: "channels", column: "model"},
		{table: "channels", column: "custom_model"},
	} {
		if db.Migrator().HasColumn(marker.table, marker.column) {
			return SchemaProfileLegacyEdge
		}
	}
	if db.Migrator().HasTable("channel_keys") {
		return SchemaProfileLegacyEdge
	}
	return SchemaProfileUpstream
}

// EnsureUpstreamSchemaCompatible prevents an upstream/C06 binary from executing
// destructive migrations against an Edge database. A later bridge migration can
// replace this guard once the multi-key/channel-model conversion is implemented.
func EnsureUpstreamSchemaCompatible(db *gorm.DB) error {
	if DetectSchemaProfile(db) != SchemaProfileLegacyEdge {
		return nil
	}
	return fmt.Errorf("legacy Edge database schema detected; refusing upstream migrations: export a config backup or run the Edge schema bridge before starting this build")
}
