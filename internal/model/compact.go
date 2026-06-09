package model

type CompactStrategy string

const (
	CompactStrategyOfficial        CompactStrategy = "official"
	CompactStrategyResponsesManual CompactStrategy = "responses_manual"
	CompactStrategyChatManual      CompactStrategy = "chat_manual"
)
