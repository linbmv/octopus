package model

import "time"

// DBDump is a full-database JSON export format for Octopus.
//
// The numeric IDs in version 1 are database-local identities rather than stable
// external identifiers. A version 1 dump can therefore only be restored into an
// empty target; it must never be merged into an existing data set.
type DBDump struct {
	Version      int       `json:"version"`
	ExportedAt   time.Time `json:"exported_at"`
	IncludeLogs  bool      `json:"include_logs"`
	IncludeStats bool      `json:"include_stats"`

	Channels    []Channel          `json:"channels,omitempty"`
	ChannelKeys []ChannelKey       `json:"channel_keys,omitempty"`
	Groups      []Group            `json:"groups,omitempty"`
	GroupItems  []GroupItem        `json:"group_items,omitempty"`
	LLMInfos    []LLMInfo          `json:"llm_infos,omitempty"`
	APIKeys     []APIKey           `json:"api_keys,omitempty"`
	Settings    []Setting          `json:"settings,omitempty"`
	Relations   *DBDumpRelationsV2 `json:"relations,omitempty"`

	StatsTotal   []StatsTotal   `json:"stats_total,omitempty"`
	StatsDaily   []StatsDaily   `json:"stats_daily,omitempty"`
	StatsHourly  []StatsHourly  `json:"stats_hourly,omitempty"`
	StatsChannel []StatsChannel `json:"stats_channel,omitempty"`
	StatsAPIKey  []StatsAPIKey  `json:"stats_api_key,omitempty"`

	RelayLogs []RelayLog `json:"relay_logs,omitempty"`
}

type DBDumpRelationsV2 struct {
	ChannelKeys map[string]string                  `json:"channel_keys"`
	GroupItems  map[string]DBDumpGroupItemRelation `json:"group_items"`
}

type DBDumpGroupItemRelation struct {
	GroupUUID       string `json:"group_uuid"`
	ChannelUUID     string `json:"channel_uuid,omitempty"`
	TargetGroupUUID string `json:"target_group_uuid,omitempty"`
}

const DBImportModeEmptyTargetRestore = "empty_target_restore"

const DBImportModeIncremental = "incremental"

type DBImportConflictPolicy string

const (
	DBImportConflictReject  DBImportConflictPolicy = "reject"
	DBImportConflictSkip    DBImportConflictPolicy = "skip"
	DBImportConflictReplace DBImportConflictPolicy = "replace"
	DBImportConflictMerge   DBImportConflictPolicy = "merge"
)

type DBImportOptions struct {
	DryRun         bool                   `json:"dry_run"`
	ConflictPolicy DBImportConflictPolicy `json:"conflict_policy"`
}

type DBImportTableSummary struct {
	Create     int64 `json:"create"`
	Update     int64 `json:"update"`
	Skip       int64 `json:"skip"`
	Delete     int64 `json:"delete"`
	Conflict   int64 `json:"conflict"`
	Unresolved int64 `json:"unresolved"`
}

type DBImportIssue struct {
	Table   string `json:"table"`
	UUID    string `json:"uuid,omitempty"`
	Field   string `json:"field,omitempty"`
	Problem string `json:"problem"`
}

type DBImportResult struct {
	// Mode makes the safety semantics explicit to API clients. Version 1 does not
	// support ID mapping or incremental merge.
	Mode string `json:"mode"`
	// DryRun reports the exact operations that would be attempted without
	// changing the target database.
	DryRun bool `json:"dry_run"`
	// ConflictPolicy is populated for version 2 incremental imports.
	ConflictPolicy DBImportConflictPolicy `json:"conflict_policy,omitempty"`
	// Tables separates planned creates, updates, skips, deletes, conflicts, and
	// unresolved references. Issues contains bounded human-readable details.
	Tables          map[string]DBImportTableSummary `json:"tables,omitempty"`
	Issues          []DBImportIssue                 `json:"issues,omitempty"`
	IssuesTruncated bool                            `json:"issues_truncated,omitempty"`
	// RowsAffected contains the rows inserted for identity-based tables and the
	// rows inserted or updated for stable-key settings.
	RowsAffected map[string]int64 `json:"rows_affected"`
	// CacheRefreshed is set by the HTTP layer after every runtime cache has been
	// reloaded successfully.
	CacheRefreshed bool `json:"cache_refreshed"`
}
