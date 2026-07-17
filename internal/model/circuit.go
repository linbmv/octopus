package model

// ChannelCircuitStatus 是渠道详情里单个熔断条目的运维快照：哪个 key 的哪个
// 模型被冻结（open/half_open）、连续失败次数以及剩余冷却秒数。
type ChannelCircuitStatus struct {
	ChannelID                int    `json:"channel_id"`
	ChannelKeyID             int    `json:"channel_key_id"`
	ModelName                string `json:"model_name"`
	State                    string `json:"state"` // open | half_open
	ConsecutiveFailures      int64  `json:"consecutive_failures"`
	TripCount                int    `json:"trip_count"`
	RemainingCooldownSeconds int    `json:"remaining_cooldown_seconds"`
}
