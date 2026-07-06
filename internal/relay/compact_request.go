package relay

import (
	"maps"
	"slices"

	"github.com/looplj/axonhub/llm"
)

// cloneRequestForAttempt 为单次通道尝试构造请求副本：浅拷贝值类型字段，
// 并深拷贝会被 transformer/middleware 原地修改的引用类型字段，避免多 attempt 互相污染。
// RawRequest 由 parsedRequestInbound.TransformRequest 每次重新赋值，不在此处拷贝。
func cloneRequestForAttempt(request *llm.Request) *llm.Request {
	if request == nil {
		return nil
	}

	attemptRequest := *request

	if request.Stream != nil {
		streamCopy := *request.Stream
		attemptRequest.Stream = &streamCopy
	}
	if request.StreamOptions != nil {
		streamOptionsCopy := *request.StreamOptions
		attemptRequest.StreamOptions = &streamOptionsCopy
	}
	if request.LogitBias != nil {
		attemptRequest.LogitBias = maps.Clone(request.LogitBias)
	}
	if request.Metadata != nil {
		attemptRequest.Metadata = maps.Clone(request.Metadata)
	}
	if request.Modalities != nil {
		attemptRequest.Modalities = slices.Clone(request.Modalities)
	}

	return &attemptRequest
}

// requestForOutboundPipeline 返回当前通道尝试要交给 pipeline 的请求副本。
func requestForOutboundPipeline(channelType llm.APIFormat, request *llm.Request) *llm.Request {
	return cloneRequestForAttempt(request)
}

// compactOfficialRequest 构造官方 /v1/responses/compact 请求副本。
func compactOfficialRequest(request *llm.Request) *llm.Request {
	attemptRequest := cloneRequestForAttempt(request)
	attemptRequest.RequestType = llm.RequestTypeCompact
	attemptRequest.APIFormat = llm.APIFormatOpenAIResponseCompact
	return attemptRequest
}
