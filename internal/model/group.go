package model

import "time"

type GroupMode int

const (
	GroupModeRoundRobin GroupMode = 1 // 轮询：依次循环选择渠道
	GroupModeRandom     GroupMode = 2 // 随机：每次随机选择一个渠道
	GroupModeFailover   GroupMode = 3 // 故障转移：按优先级选择，失败时降级到下一个
	GroupModeWeighted   GroupMode = 4 // 加权分配：按优权重分配流量
)

type Group struct {
	ID   int    `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"unique;not null"`
	// Enabled 临时启用/禁用整个分组（不影响成员和渠道本身）。零值经迁移回填为 true，存量与新建分组默认启用。
	Enabled           bool        `json:"enabled" gorm:"not null;default:true"`
	Mode              GroupMode   `json:"mode" gorm:"not null"`
	MatchRegex        string      `json:"match_regex"`
	FirstTokenTimeOut int         `json:"first_token_time_out"` // 单个渠道首个Token响应超时时间(秒)
	SessionKeepTime   int         `json:"session_keep_time"`    // 会话保持时间(秒) 0 为禁用
	Items             []GroupItem `json:"items,omitempty" gorm:"foreignKey:GroupID"`
}

const (
	GroupItemTypeChannel = "channel"
	GroupItemTypeGroup   = "group"
)

type GroupItem struct {
	ID            int    `json:"id" gorm:"primaryKey"`
	GroupID       int    `json:"group_id" gorm:"not null;index:idx_group_item_unique,unique"`
	Type          string `json:"type" gorm:"not null;default:channel;index:idx_group_item_unique,unique"`
	ChannelID     int    `json:"channel_id" gorm:"index:idx_group_item_unique,unique"`
	TargetGroupID int    `json:"target_group_id" gorm:"index:idx_group_item_unique,unique"`
	ModelName     string `json:"model_name" gorm:"index:idx_group_item_unique,unique"`
	Priority      int    `json:"priority"`
	Weight        int    `json:"weight"`
	// Disabled 临时禁用该分组成员（不影响渠道本身）。零值 false 表示启用，存量数据与新建成员默认启用。
	Disabled bool `json:"disabled" gorm:"not null;default:false"`

	CompactStrategy          CompactStrategy `json:"-" gorm:"type:varchar(32);default:''"`
	CompactStrategyUpdatedAt *time.Time      `json:"-"`
	CompactProbeError        string          `json:"-" gorm:"type:text"`
}

// GroupUpdateRequest 分组更新请求 - 仅包含变更的数据
type GroupUpdateRequest struct {
	ID                int                      `json:"id" binding:"required"`
	Name              *string                  `json:"name,omitempty"`                 // 仅在名称变更时发送
	Enabled           *bool                    `json:"enabled,omitempty"`              // 仅在启用状态变更时发送
	Mode              *GroupMode               `json:"mode,omitempty"`                 // 仅在模式变更时发送
	MatchRegex        *string                  `json:"match_regex,omitempty"`          // 仅在匹配正则变更时发送
	FirstTokenTimeOut *int                     `json:"first_token_time_out,omitempty"` // 仅在超时变更时发送(秒)
	SessionKeepTime   *int                     `json:"session_keep_time,omitempty"`    // 仅在会话保持时间变更时发送(秒)
	ItemsToAdd        []GroupItemAddRequest    `json:"items_to_add,omitempty"`         // 新增的 items
	ItemsToUpdate     []GroupItemUpdateRequest `json:"items_to_update,omitempty"`      // 更新的 items (priority 变更)
	ItemsToDelete     []int                    `json:"items_to_delete,omitempty"`      // 删除的 item IDs
}

// GroupItemAddRequest 新增 item 请求
type GroupItemAddRequest struct {
	Type          string `json:"type,omitempty"`
	ChannelID     int    `json:"channel_id,omitempty"`
	TargetGroupID int    `json:"target_group_id,omitempty"`
	ModelName     string `json:"model_name,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	Weight        int    `json:"weight,omitempty"`
}

// GroupItemUpdateRequest 更新 item 请求
type GroupItemUpdateRequest struct {
	ID       int   `json:"id" binding:"required"`
	Priority int   `json:"priority,omitempty"`
	Weight   int   `json:"weight,omitempty"`
	Disabled *bool `json:"disabled,omitempty"` // 仅在禁用状态变更时发送
}
type GroupIDAndLLMName struct {
	ChannelID int
	ModelName string
}
