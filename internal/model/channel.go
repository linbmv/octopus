package model

import (
	"sort"
	"time"

	"github.com/looplj/axonhub/llm"
)

type AutoGroupType int

const (
	AutoGroupTypeNone  AutoGroupType = 0 //不自动分组
	AutoGroupTypeFuzzy AutoGroupType = 1 //模糊匹配
	AutoGroupTypeExact AutoGroupType = 2 //准确匹配
	AutoGroupTypeRegex AutoGroupType = 3 //正则匹配
)

const ChannelTypeDoubao llm.APIFormat = "doubao"

type Channel struct {
	ID               int               `json:"id" gorm:"primaryKey"`
	UUID             string            `json:"uuid,omitempty" gorm:"size:36;uniqueIndex"`
	Name             string            `json:"name" gorm:"unique;not null" binding:"required,min=1,max=64"`
	Type             llm.APIFormat     `json:"type" binding:"required"`
	Enabled          bool              `json:"enabled" gorm:"default:true"`
	BaseUrls         []BaseUrl         `json:"base_urls" gorm:"serializer:json" binding:"required,min=1,dive"`
	Keys             []ChannelKey      `json:"keys" gorm:"foreignKey:ChannelID"`
	Model            string            `json:"model"`
	CustomModel      string            `json:"custom_model"`
	Proxy            bool              `json:"proxy" gorm:"default:false"`
	AutoSync         bool              `json:"auto_sync" gorm:"default:false"`
	AutoGroup        AutoGroupType     `json:"auto_group" gorm:"default:0"`
	CustomHeader     []CustomHeader    `json:"custom_header" gorm:"serializer:json"`
	HeaderRules      []HeaderRule      `json:"header_rules" gorm:"serializer:json"`
	JSONRewriteRules []JSONRewriteRule `json:"json_rewrite_rules" gorm:"serializer:json"`
	ParamOverride    *string           `json:"param_override"`
	RawPassthrough   bool              `json:"raw_passthrough" gorm:"not null;default:false"`
	RPMLimit         int               `json:"rpm_limit" gorm:"not null;default:0" binding:"gte=0"`
	MaxConcurrency   int               `json:"max_concurrency" gorm:"not null;default:0" binding:"gte=0"`
	ChannelProxy     *string           `json:"channel_proxy"`
	Stats            *StatsChannel     `json:"stats,omitempty" gorm:"foreignKey:ChannelID"`
	MatchRegex       *string           `json:"match_regex"`
	// UserAgent 覆盖该渠道出站请求的 User-Agent。留空则用默认浏览器 UA。
	// 部分上游按客户端标识放行（如"仅 Claude Code 客户端"的中转站），需在此填对应 UA。
	UserAgent string `json:"user_agent" gorm:"default:''"`
}

type BaseUrl struct {
	URL   string `json:"url" binding:"required,url"`
	Delay int    `json:"delay" binding:"gte=0"`
}

type CustomHeader struct {
	HeaderKey   string `json:"header_key"`
	HeaderValue string `json:"header_value"`
}

// HeaderRule is an ordered advanced rewrite. CustomHeader remains the
// backwards-compatible simple "set" form.
type HeaderRule struct {
	Action      string `json:"action"`
	HeaderKey   string `json:"header_key"`
	HeaderValue string `json:"header_value,omitempty"`
}

// JSONRewriteRule uses the bounded JSON Pointer subset documented by
// requestrewrite.ParseJSONPointer. Value contains one encoded JSON value and is
// required only for override.
type JSONRewriteRule struct {
	Action string  `json:"action"`
	Path   string  `json:"path"`
	Value  *string `json:"value,omitempty"`
}

type ChannelKey struct {
	ID               int     `json:"id" gorm:"primaryKey"`
	UUID             string  `json:"uuid,omitempty" gorm:"size:36;uniqueIndex"`
	ChannelID        int     `json:"channel_id"`
	Enabled          bool    `json:"enabled" gorm:"default:true"`
	ChannelKey       string  `json:"channel_key"`
	StatusCode       int     `json:"status_code"`
	LastUseTimeStamp int64   `json:"last_use_time_stamp"`
	TotalCost        float64 `json:"total_cost"`
	Remark           string  `json:"remark"`
}

// ChannelUpdateRequest 渠道更新请求 - 仅包含变更的数据
type ChannelUpdateRequest struct {
	ID               int                `json:"id" binding:"required"`
	Name             *string            `json:"name,omitempty"`
	Type             *llm.APIFormat     `json:"type,omitempty"`
	Enabled          *bool              `json:"enabled,omitempty"`
	BaseUrls         *[]BaseUrl         `json:"base_urls,omitempty"`
	Model            *string            `json:"model,omitempty"`
	CustomModel      *string            `json:"custom_model,omitempty"`
	Proxy            *bool              `json:"proxy,omitempty"`
	AutoSync         *bool              `json:"auto_sync,omitempty"`
	AutoGroup        *AutoGroupType     `json:"auto_group,omitempty"`
	CustomHeader     *[]CustomHeader    `json:"custom_header,omitempty"`
	HeaderRules      *[]HeaderRule      `json:"header_rules,omitempty"`
	JSONRewriteRules *[]JSONRewriteRule `json:"json_rewrite_rules,omitempty"`
	ChannelProxy     *string            `json:"channel_proxy,omitempty"`
	ParamOverride    *string            `json:"param_override,omitempty"`
	RawPassthrough   *bool              `json:"raw_passthrough,omitempty"`
	RPMLimit         *int               `json:"rpm_limit,omitempty"`
	MaxConcurrency   *int               `json:"max_concurrency,omitempty"`
	MatchRegex       *string            `json:"match_regex,omitempty"`
	UserAgent        *string            `json:"user_agent,omitempty"`

	KeysToAdd    []ChannelKeyAddRequest    `json:"keys_to_add,omitempty"`
	KeysToUpdate []ChannelKeyUpdateRequest `json:"keys_to_update,omitempty"`
	KeysToDelete []int                     `json:"keys_to_delete,omitempty"`
}

type ChannelKeyAddRequest struct {
	Enabled    bool   `json:"enabled"`
	ChannelKey string `json:"channel_key" binding:"required"`
	Remark     string `json:"remark"`
}

type ChannelKeyUpdateRequest struct {
	ID         int     `json:"id" binding:"required"`
	Enabled    *bool   `json:"enabled,omitempty"`
	ChannelKey *string `json:"channel_key,omitempty"`
	Remark     *string `json:"remark,omitempty"`
}

func (c *Channel) GetBaseUrl() string {
	if c == nil || len(c.BaseUrls) == 0 {
		return ""
	}

	bestURL := ""
	bestDelay := 0
	bestSet := false

	for _, bu := range c.BaseUrls {
		if bu.URL == "" {
			continue
		}
		if !bestSet || bu.Delay < bestDelay {
			bestURL = bu.URL
			bestDelay = bu.Delay
			bestSet = true
		}
	}

	return bestURL
}

// IsAvailable 判断 key 是否可参与调度。
// 仅对 429（限流）做 5 分钟冷却；其余失败状态码（如 400/401/403/503）不在此永久禁用 key，
// 这类失败可能是上游偶发、请求参数或临时故障，连续失败由熔断器（带自动恢复）处理，避免一次偶发就把可用 key 永久排除。
func (k ChannelKey) IsAvailable(nowSec int64) bool {
	if !k.Enabled || k.ChannelKey == "" {
		return false
	}
	if k.StatusCode == 429 && k.LastUseTimeStamp > 0 {
		return nowSec-k.LastUseTimeStamp >= int64(5*time.Minute/time.Second)
	}
	return true
}

func (c *Channel) GetChannelKeyByID(keyID int) ChannelKey {
	if c == nil || keyID == 0 || len(c.Keys) == 0 {
		return ChannelKey{}
	}

	nowSec := time.Now().Unix()
	for _, k := range c.Keys {
		if k.ID == keyID && k.IsAvailable(nowSec) {
			return k
		}
	}
	return ChannelKey{}
}

func (c *Channel) GetChannelKey() ChannelKey {
	if c == nil || len(c.Keys) == 0 {
		return ChannelKey{}
	}

	nowSec := time.Now().Unix()

	best := ChannelKey{}
	bestCost := 0.0
	bestSet := false

	for _, k := range c.Keys {
		if !k.IsAvailable(nowSec) {
			continue
		}
		if !bestSet || k.TotalCost < bestCost {
			best = k
			bestCost = k.TotalCost
			bestSet = true
		}
	}

	if !bestSet {
		return ChannelKey{}
	}
	return best
}

func (c *Channel) AvailableKeys() []ChannelKey {
	if c == nil || len(c.Keys) == 0 {
		return nil
	}
	nowSec := time.Now().Unix()
	keys := make([]ChannelKey, 0, len(c.Keys))
	for _, key := range c.Keys {
		if key.IsAvailable(nowSec) {
			keys = append(keys, key)
		}
	}
	return keys
}

func (c *Channel) AvailableKeysForAttempt(stickyKeyID int) []ChannelKey {
	keys := c.AvailableKeys()
	if len(keys) <= 1 {
		return keys
	}

	result := make([]ChannelKey, 0, len(keys))
	if stickyKeyID > 0 {
		for _, key := range keys {
			if key.ID == stickyKeyID {
				result = append(result, key)
				break
			}
		}
	}

	remaining := make([]ChannelKey, 0, len(keys))
	for _, key := range keys {
		if stickyKeyID > 0 && key.ID == stickyKeyID {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		if remaining[i].TotalCost == remaining[j].TotalCost {
			return remaining[i].ID < remaining[j].ID
		}
		return remaining[i].TotalCost < remaining[j].TotalCost
	})
	result = append(result, remaining...)
	return result
}
