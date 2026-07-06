package model

import "github.com/looplj/axonhub/llm"

type CompactStrategy string

const (
	CompactStrategyOfficial     CompactStrategy = "official"
	CompactStrategyIncompatible CompactStrategy = "incompatible"
)

// CompactStrategyOrder is the single source of truth for supported remote
// compact strategies per channel type.
func CompactStrategyOrder(format llm.APIFormat) []CompactStrategy {
	switch format {
	case llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIResponseCompact:
		return []CompactStrategy{CompactStrategyOfficial}
	default:
		return nil
	}
}
