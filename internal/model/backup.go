package model

import "time"

// DBDump is a full-database JSON export format for Octopus.
// Import uses incremental semantics (insert new rows, and upsert on tables with natural keys).
type DBDump struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Scope      string    `json:"scope,omitempty"` // 导出范围；config 表示仅配置，不含日志、统计或模型价格。

	Channels      []Channel      `json:"channels,omitempty"`       // 渠道数据。
	ChannelKeys   []ChannelKey   `json:"channel_keys,omitempty"`   // 渠道多凭据数据。
	ChannelModels []ChannelModel `json:"channel_models,omitempty"` // 渠道模型数据。
	Groups        []Group        `json:"groups,omitempty"`         // 分组数据。
	GroupItems    []GroupItem    `json:"group_items,omitempty"`    // 分组成员数据。
	LLMInfos      []LLMInfo      `json:"llm_infos,omitempty"`      // 模型价格数据。
	APIKeys       []APIKey       `json:"api_keys,omitempty"`       // API Key 数据。
	Settings      []Setting      `json:"settings,omitempty"`       // 系统设置数据。

	StatsTotal  []StatsTotal  `json:"stats_total,omitempty"`
	StatsDaily  []StatsDaily  `json:"stats_daily,omitempty"`
	StatsHourly []StatsHourly `json:"stats_hourly,omitempty"`
	StatsAPIKey []StatsAPIKey `json:"stats_api_key,omitempty"`
}

// ConfigDump 是跨部署迁移使用的配置备份格式。
// 与 DBDump 不同，它有意不携带统计字段，也不携带 LLM 价格缓存，
// 避免把运行时历史数据误当成渠道配置导入新实例。
type ConfigDump struct {
	Version    int       `json:"version"`
	Scope      string    `json:"scope"`
	ExportedAt time.Time `json:"exported_at"`

	Channels      []ConfigChannel      `json:"channels,omitempty"`
	ChannelModels []ConfigChannelModel `json:"channel_models,omitempty"`
	Groups        []ConfigGroup        `json:"groups,omitempty"`
	GroupItems    []ConfigGroupItem    `json:"group_items,omitempty"`
	APIKeys       []APIKey             `json:"api_keys,omitempty"`
	Settings      []Setting            `json:"settings,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
}

// ConfigChannel 与 Channel 保持配置字段一致，但刻意排除 StatsMetrics。
type ConfigChannel struct {
	ID               int                `json:"id"`
	Name             string             `json:"name"`
	Type             ChannelProvider    `json:"type"`
	Enabled          bool               `json:"enabled"`
	BaseURL          string             `json:"base_url"`
	BaseUrls         []BaseUrl          `json:"base_urls,omitempty"`
	Key              string             `json:"key"`
	Keys             []ConfigChannelKey `json:"keys,omitempty"`
	Proxy            bool               `json:"proxy"`
	AutoSync         bool               `json:"auto_sync"`
	CustomHeader     []CustomHeader     `json:"custom_header"`
	HeaderRules      []HeaderRule       `json:"header_rules,omitempty"`
	JSONRewriteRules []JSONRewriteRule  `json:"json_rewrite_rules,omitempty"`
	ParamOverride    *string            `json:"param_override,omitempty"`
	ChannelProxy     *string            `json:"channel_proxy,omitempty"`
	MatchRegex       *string            `json:"match_regex,omitempty"`
}

// ConfigChannelKey is the configuration-only representation of a channel key.
// Runtime counters are intentionally omitted; status fields are retained only
// so an imported key can preserve provider cooldown state when present.
type ConfigChannelKey struct {
	ID               int    `json:"id"`
	ChannelID        int    `json:"channel_id"`
	Enabled          bool   `json:"enabled"`
	ChannelKey       string `json:"channel_key"`
	StatusCode       int    `json:"status_code,omitempty"`
	LastUseTimeStamp int64  `json:"last_use_time_stamp,omitempty"`
	RetryAfterUntil  int64  `json:"retry_after_until,omitempty"`
	Remark           string `json:"remark,omitempty"`
}

// ConfigChannelModel 与 ChannelModel 保持配置字段一致，但刻意排除统计字段。
type ConfigChannelModel struct {
	ID        int                `json:"id"`
	ChannelID int                `json:"channel_id"`
	Name      string             `json:"name"`
	Source    ChannelModelSource `json:"source"`
}

// ConfigGroup 只包含分组配置，不携带预加载关联对象。
type ConfigGroup struct {
	ID           int              `json:"id"`
	Name         string           `json:"name"`
	Enabled      bool             `json:"enabled"`
	Mode         GroupMode        `json:"mode"`
	ActiveItemID int              `json:"active_item_id"`
	RelayConfig  GroupRelayConfig `json:"relay_config"`
}

// ConfigGroupItem 只包含成员关系，支持渠道模型和嵌套分组。
type ConfigGroupItem struct {
	ID             int           `json:"id"`
	GroupID        int           `json:"group_id"`
	Type           GroupItemType `json:"type"`
	ChannelModelID *int          `json:"channel_model_id,omitempty"`
	TargetGroupID  *int          `json:"target_group_id,omitempty"`
	Priority       int           `json:"priority"`
	Weight         int           `json:"weight"`
	Disabled       bool          `json:"disabled"`
}

const ConfigDumpScope = "config"

type DBImportResult struct {
	// RowsAffected contains the rows affected for each table operation (insert/upsert depending on table).
	RowsAffected map[string]int64 `json:"rows_affected"`
	Warnings     []string         `json:"warnings,omitempty"`
}
