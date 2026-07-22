package model

type StatsMetrics struct {
	InputToken     int64   `json:"input_token" gorm:"bigint"`
	OutputToken    int64   `json:"output_token" gorm:"bigint"`
	ReasoningToken int64   `json:"reasoning_token" gorm:"type:bigint;not null;default:0"`
	InputCost      float64 `json:"input_cost" gorm:"type:real"`
	OutputCost     float64 `json:"output_cost" gorm:"type:real"`
	WaitTime       int64   `json:"wait_time" gorm:"bigint"`
	RequestSuccess int64   `json:"request_success" gorm:"bigint"`
	RequestFailed  int64   `json:"request_failed" gorm:"bigint"`
}

type StatsTotal struct {
	ID int `gorm:"primaryKey"`
	StatsMetrics
}

type StatsHourly struct {
	Hour int    `json:"hour" gorm:"primaryKey"`
	Date string `json:"date" gorm:"not null"` // 记录最后更新日期，格式：20060102
	StatsMetrics
}

type StatsDaily struct {
	Date string `json:"date" gorm:"primaryKey"`
	StatsMetrics
}

type StatsChannel struct {
	ChannelID int `json:"channel_id" gorm:"primaryKey"`
	StatsMetrics
}

type StatsAPIKey struct {
	APIKeyID int `json:"api_key_id" gorm:"primaryKey"`
	StatsMetrics
}

// StatsErrorLevelCounts is the aggregate of classified failed relay attempts.
// Unclassified legacy attempts and non-failure decisions are deliberately not
// folded into these counters.
type StatsErrorLevelCounts struct {
	Key     int64 `json:"key"`
	Channel int64 `json:"channel"`
	Client  int64 `json:"client"`
}

type StatsErrorLevelTrendPoint struct {
	BucketStart int64 `json:"bucket_start"`
	StatsErrorLevelCounts
}

// StatsErrorLevels describes a bounded, time-windowed scan over RelayLog
// attempts. Truncated tells callers that Capacity newest logs were used and
// older logs inside the requested window were intentionally excluded.
type StatsErrorLevels struct {
	From        int64                       `json:"from"`
	To          int64                       `json:"to"`
	WindowHours int                         `json:"window_hours"`
	ChannelID   int                         `json:"channel_id,omitempty"`
	ScannedLogs int                         `json:"scanned_logs"`
	Capacity    int                         `json:"capacity"`
	Truncated   bool                        `json:"truncated"`
	Counts      StatsErrorLevelCounts       `json:"counts"`
	Trend       []StatsErrorLevelTrendPoint `json:"trend"`
}

// Add aggregates another StatsMetrics into the current one.
func (s *StatsMetrics) Add(delta StatsMetrics) {
	s.InputToken += delta.InputToken
	s.OutputToken += delta.OutputToken
	s.ReasoningToken += delta.ReasoningToken
	s.InputCost += delta.InputCost
	s.OutputCost += delta.OutputCost
	s.WaitTime += delta.WaitTime
	s.RequestSuccess += delta.RequestSuccess
	s.RequestFailed += delta.RequestFailed
}
