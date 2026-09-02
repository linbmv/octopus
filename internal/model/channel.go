package model

import (
	"strings"
	"time"
)

// 渠道使用的上游服务提供方。
type ChannelProvider string

const (
	ChannelProviderOpenAI          ChannelProvider = "openai"
	ChannelProviderOpenAIResponses ChannelProvider = "openai_responses"
	ChannelProviderAnthropic       ChannelProvider = "anthropic"
	ChannelProviderGemini          ChannelProvider = "gemini"
	ChannelProviderVolcengine      ChannelProvider = "volcengine"
)

// 渠道模型的来源类型。
type ChannelModelSource string

const (
	ChannelModelSourceAuto   ChannelModelSource = "auto"   // 通过上游接口自动获取。
	ChannelModelSourceManual ChannelModelSource = "manual" // 管理员手动配置。
)

// 单个上游渠道的连接和转发配置。
type Channel struct {
	ID               int               `json:"id" gorm:"primaryKey"`                                                     // 渠道主键。
	Name             string            `json:"name" gorm:"unique;not null"`                                              // 渠道名称。
	Type             ChannelProvider   `json:"type"`                                                                     // 上游服务提供方。
	Enabled          bool              `json:"enabled" gorm:"default:true"`                                              // 渠道是否可用。
	BaseURL          string            `json:"base_url"`                                                                 // 唯一的上游基础地址。
	BaseUrls         []BaseUrl         `json:"base_urls,omitempty" gorm:"serializer:json"`                               // 兼容 Edge 的多上游地址；BaseURL 保留为当前主地址。
	Key              string            `json:"key"`                                                                      // 唯一的上游访问凭据。
	Keys             []ChannelKey      `json:"keys,omitempty" gorm:"foreignKey:ChannelID;constraint:OnDelete:CASCADE"`   // 兼容 Edge 的多凭据；Key 保留为当前主凭据。
	Models           []ChannelModel    `json:"models,omitempty" gorm:"foreignKey:ChannelID;constraint:OnDelete:CASCADE"` // 渠道提供的模型。
	Proxy            bool              `json:"proxy" gorm:"default:false"`                                               // 是否使用代理。
	AutoSync         bool              `json:"auto_sync" gorm:"default:false"`                                           // 是否自动同步模型。
	CustomHeader     []CustomHeader    `json:"custom_header" gorm:"serializer:json"`                                     // 追加到上游请求的 Header。
	HeaderRules      []HeaderRule      `json:"header_rules,omitempty" gorm:"serializer:json"`                            // 有序的高级 Header 改写规则。
	JSONRewriteRules []JSONRewriteRule `json:"json_rewrite_rules,omitempty" gorm:"serializer:json"`                      // 有序的请求体 JSON 改写规则。
	ParamOverride    *string           `json:"param_override"`                                                           // 请求参数覆盖配置。
	ChannelProxy     *string           `json:"channel_proxy"`                                                            // 渠道专用代理地址。
	MatchRegex       *string           `json:"match_regex"`                                                              // 模型同步过滤表达式。
	StatsMetrics                       // 渠道累计统计信息。
}

// BaseUrl 是一个带可选延迟提示的上游地址。
type BaseUrl struct {
	URL   string `json:"url"`
	Delay int    `json:"delay,omitempty"`
}

// ChannelKey 保存渠道的独立凭据。运行时优先从 Keys 中选择具体凭据，
// 没有独立凭据时回退到 Channel.Key；两者并存保证旧 API/Relay 可渐进迁移。
type ChannelKey struct {
	ID               int    `json:"id" gorm:"primaryKey"`
	ChannelID        int    `json:"channel_id" gorm:"not null;index"`
	Enabled          bool   `json:"enabled" gorm:"not null;default:true"`
	ChannelKey       string `json:"channel_key" gorm:"not null"`
	StatusCode       int    `json:"status_code,omitempty"`
	LastUseTimeStamp int64  `json:"last_use_time_stamp,omitempty"`
	RetryAfterUntil  int64  `json:"retry_after_until,omitempty"`
	Remark           string `json:"remark,omitempty"`
}

// ChannelCircuitStatus 是渠道运行态中单个熔断条目的安全快照，不包含凭据值。
type ChannelCircuitStatus struct {
	ChannelID                int    `json:"channel_id"`
	ChannelKeyID             int    `json:"channel_key_id"`
	ModelName                string `json:"model_name"`
	State                    string `json:"state"` // open | half_open
	ConsecutiveFailures      int    `json:"consecutive_failures"`
	TripCount                int    `json:"trip_count"`
	RemainingCooldownSeconds int    `json:"remaining_cooldown_seconds,omitempty"`
	InFlightProbes           int    `json:"in_flight_probes,omitempty"`
}

// IsAvailable reports whether a credential may be selected for a request.
// Only explicit 429 cooldown state suppresses a key; other status codes remain
// eligible because transient provider errors are handled by relay failover.
func (k ChannelKey) IsAvailable(nowSec int64) bool {
	if !k.Enabled || strings.TrimSpace(k.ChannelKey) == "" {
		return false
	}
	if k.StatusCode == 429 {
		if k.RetryAfterUntil > 0 {
			return nowSec >= k.RetryAfterUntil
		}
		if k.LastUseTimeStamp > 0 {
			return nowSec-k.LastUseTimeStamp >= int64(5*time.Minute/time.Second)
		}
	}
	return true
}

// 渠道提供的单个上游模型。
type ChannelModel struct {
	ID           int                `json:"id" gorm:"primaryKey"`                                           // 渠道模型主键。
	ChannelID    int                `json:"channel_id" gorm:"not null;index:idx_channel_model_name,unique"` // 所属渠道 ID。
	Name         string             `json:"name" gorm:"not null;index:idx_channel_model_name,unique"`       // 上游模型名称。
	Source       ChannelModelSource `json:"source" gorm:"not null;default:auto"`                            // 模型来源。
	StatsMetrics                    // 渠道模型统计信息。
}

// 追加到上游请求的单个 Header。
type CustomHeader struct {
	HeaderKey   string `json:"header_key"`   // Header 名称。
	HeaderValue string `json:"header_value"` // Header 值。
}

// HeaderRule 是一条有序的高级 Header 改写规则。CustomHeader 保留为简单的
// "设置" 写法, HeaderRule 额外支持 append 与 remove, 在 CustomHeader 之后按序生效。
type HeaderRule struct {
	Action      string `json:"action"`                 // set、append 或 remove。
	HeaderKey   string `json:"header_key"`             // Header 名称。
	HeaderValue string `json:"header_value,omitempty"` // remove 时忽略。
}

// JSONRewriteRule 使用 requestrewrite.ParseJSONPointer 限定的 JSON Pointer 子集。
// Value 是单个已编码的 JSON 值, 仅 override 需要。
type JSONRewriteRule struct {
	Action string  `json:"action"`          // override 或 remove。
	Path   string  `json:"path"`            // JSON Pointer, 例如 /tools/0/type。
	Value  *string `json:"value,omitempty"` // override 时必填的 JSON 值。
}

// ChannelUpdateRequest 渠道更新请求 - 仅包含变更的数据。
type ChannelUpdateRequest struct {
	ID               int                       `json:"id" binding:"required"`        // 待更新渠道的主键。
	Name             *string                   `json:"name,omitempty"`               // 新的渠道名称。
	Type             *ChannelProvider          `json:"type,omitempty"`               // 新的上游服务提供方。
	Enabled          *bool                     `json:"enabled,omitempty"`            // 新的启用状态。
	BaseURL          *string                   `json:"base_url,omitempty"`           // 新的上游基础地址。
	BaseUrls         *[]BaseUrl                `json:"base_urls,omitempty"`          // 多上游地址配置。
	Key              *string                   `json:"key,omitempty"`                // 新的上游访问凭据。
	KeysToAdd        []ChannelKeyAddRequest    `json:"keys_to_add,omitempty"`        // 新增渠道凭据。
	KeysToUpdate     []ChannelKeyUpdateRequest `json:"keys_to_update,omitempty"`     // 更新渠道凭据。
	KeysToDelete     []int                     `json:"keys_to_delete,omitempty"`     // 删除渠道凭据。
	Models           *[]ChannelModel           `json:"models,omitempty"`             // 新的渠道模型集合。
	Proxy            *bool                     `json:"proxy,omitempty"`              // 新的代理开关。
	AutoSync         *bool                     `json:"auto_sync,omitempty"`          // 新的自动同步开关。
	CustomHeader     *[]CustomHeader           `json:"custom_header,omitempty"`      // 新的自定义 Header。
	HeaderRules      *[]HeaderRule             `json:"header_rules,omitempty"`       // 新的高级 Header 改写规则。
	JSONRewriteRules *[]JSONRewriteRule        `json:"json_rewrite_rules,omitempty"` // 新的请求体 JSON 改写规则。
	ChannelProxy     *string                   `json:"channel_proxy,omitempty"`      // 新的渠道代理地址。
	ParamOverride    *string                   `json:"param_override,omitempty"`     // 新的参数覆盖配置。
	MatchRegex       *string                   `json:"match_regex,omitempty"`        // 新的模型过滤表达式。
}

type ChannelKeyAddRequest struct {
	Enabled    bool   `json:"enabled"`
	ChannelKey string `json:"channel_key"`
	Remark     string `json:"remark,omitempty"`
}

type ChannelKeyUpdateRequest struct {
	ID         int     `json:"id"`
	Enabled    *bool   `json:"enabled,omitempty"`
	ChannelKey *string `json:"channel_key,omitempty"`
	Remark     *string `json:"remark,omitempty"`
}
