package model

import "github.com/looplj/axonhub/llm"

type CompactStrategy string

const (
	CompactStrategyOfficial        CompactStrategy = "official"
	CompactStrategyResponsesManual CompactStrategy = "responses_manual"
	CompactStrategyChatManual      CompactStrategy = "chat_manual"
	CompactStrategyIncompatible    CompactStrategy = "incompatible"
)

// CompactStrategyOrder is the single source of truth for the canonical full
// compact fallback order per channel type.
func CompactStrategyOrder(format llm.APIFormat) []CompactStrategy {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return []CompactStrategy{CompactStrategyChatManual}
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		return []CompactStrategy{
			CompactStrategyOfficial,
			CompactStrategyResponsesManual,
			CompactStrategyChatManual,
		}
	default:
		return nil
	}
}
