package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	observability "github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// RelayMetrics 负责最终的日志收集与持久化
type RelayMetrics struct {
	APIKeyID     int
	RequestModel string
	StartTime    time.Time

	// 首 Token 时间
	FirstTokenTime time.Time

	// 请求和最终响应体；InternalResponse 保存实际写回客户端或流式聚合后的 body，不再强制转换成 llm.Response。
	InternalRequest  *llm.Request
	InternalResponse []byte

	// 统计指标
	ActualModel string
	Stats       model.StatsMetrics

	// 参数覆盖
	ParamOverride string

	// 出站请求摘要；raw passthrough 会绕过标准化 llm.Request，日志需记录最终出站语义摘要用于审计。
	OutboundRequestSummary *OutboundRequestSummary
	// OutboundRequestArtifact is the redacted final request shape captured after
	// channel rewrites. It is kept in memory for the future baseline sink and is
	// never serialized into the ordinary relay log automatically.
	OutboundRequestArtifact *requestartifact.Artifact
}

type OutboundRequestSummary struct {
	RawPassthrough        bool `json:"raw_passthrough"`
	ParamOverrideApplied  bool `json:"param_override_applied,omitempty"`
	JSONRewriteApplied    bool `json:"json_rewrite_applied,omitempty"`
	HeaderRewriteApplied  bool `json:"header_rewrite_applied,omitempty"`
	RequestRewriteApplied bool `json:"request_rewrite_applied,omitempty"`
	BodyBytes             int  `json:"body_bytes"`
	// BodySHA256 hashes the exact outbound body bytes so logs can be compared
	// against captured upstream traffic; ShapeSHA256 hashes the redacted shape.
	BodySHA256    string         `json:"body_sha256"`
	ShapeSHA256   string         `json:"shape_sha256,omitempty"`
	Model         string         `json:"model,omitempty"`
	Stream        *bool          `json:"stream,omitempty"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
}

func (m *RelayMetrics) RecordUsage(usage *llm.Usage) {
	if usage == nil {
		return
	}

	// usage 已由 axonhub/llm 标准化；octopus 仍使用本地模型价格表计算成本，所以这里只做用量落点和价格换算。
	m.Stats.InputToken = usage.PromptTokens
	m.Stats.OutputToken = usage.CompletionTokens
	m.Stats.ReasoningToken = normalizedReasoningTokens(usage)

	modelPrice := price.GetLLMPrice(m.ActualModel)
	if modelPrice == nil {
		return
	}
	tokenDetails := usage.PromptTokensDetails
	if tokenDetails == nil {
		tokenDetails = &llm.PromptTokensDetails{}
	}
	// 缓存读、缓存写和普通输入的单价不同；如果上游返回的缓存明细超过总输入 token，就退回按全部输入 token 计费，避免出现负成本。
	nonCachedTokens := usage.PromptTokens - tokenDetails.CachedTokens - tokenDetails.WriteCachedTokens
	if nonCachedTokens < 0 {
		nonCachedTokens = usage.PromptTokens
	}
	m.Stats.InputCost = (float64(tokenDetails.CachedTokens)*modelPrice.CacheRead +
		float64(tokenDetails.WriteCachedTokens)*modelPrice.CacheWrite +
		float64(nonCachedTokens)*modelPrice.Input) * 1e-6
	m.Stats.OutputCost = float64(usage.CompletionTokens) * modelPrice.Output * 1e-6
}

func normalizedReasoningTokens(usage *llm.Usage) int64 {
	if usage == nil || usage.CompletionTokensDetails == nil {
		return 0
	}
	reasoning := usage.CompletionTokensDetails.ReasoningTokens
	if reasoning < 0 {
		return 0
	}
	if usage.CompletionTokens >= 0 && reasoning > usage.CompletionTokens {
		return usage.CompletionTokens
	}
	return reasoning
}

func (m *RelayMetrics) RecordOutboundRequestSummary(
	request *httpclient.Request,
	rawPassthrough bool,
	paramOverrideApplied bool,
	jsonRewriteApplied bool,
	headerRewriteApplied bool,
) {
	if request == nil {
		return
	}
	// The summary is audit metadata on the hot path: it hashes and inspects the
	// body directly instead of building a full requestartifact shape. The
	// redacted ShapeSHA256 is backfilled by the pipeline middleware after codex
	// stripping, when self-healing has artifact capture enabled.
	bodyHash := sha256.Sum256(request.Body)
	summary := &OutboundRequestSummary{
		RawPassthrough:        rawPassthrough,
		ParamOverrideApplied:  paramOverrideApplied,
		JSONRewriteApplied:    jsonRewriteApplied,
		HeaderRewriteApplied:  headerRewriteApplied,
		RequestRewriteApplied: paramOverrideApplied || jsonRewriteApplied || headerRewriteApplied,
		BodyBytes:             len(request.Body),
		BodySHA256:            hex.EncodeToString(bodyHash[:]),
	}

	var bodyMap map[string]any
	if err := json.Unmarshal(request.Body, &bodyMap); err == nil {
		if model, ok := bodyMap["model"].(string); ok {
			summary.Model = model
		}
		if stream, ok := bodyMap["stream"].(bool); ok {
			summary.Stream = &stream
		}
		if streamOptions, ok := bodyMap["stream_options"].(map[string]any); ok {
			// Only the protocol boolean needed to explain usage accounting is safe
			// metadata. Never copy arbitrary extension values from a client body.
			if includeUsage, ok := streamOptions["include_usage"].(bool); ok {
				summary.StreamOptions = map[string]any{"include_usage": includeUsage}
			}
		}
	}

	m.OutboundRequestSummary = summary
}

func (m *RelayMetrics) Save(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt) {
	duration := time.Since(m.StartTime)

	globalStats := model.StatsMetrics{
		WaitTime:       duration.Milliseconds(),
		InputToken:     m.Stats.InputToken,
		OutputToken:    m.Stats.OutputToken,
		ReasoningToken: m.Stats.ReasoningToken,
		InputCost:      m.Stats.InputCost,
		OutputCost:     m.Stats.OutputCost,
	}
	if success {
		globalStats.RequestSuccess = 1
	} else {
		globalStats.RequestFailed = 1
	}

	channelID, channelName, finalKeyID, finalStatus := finalAttempt(attempts)
	observability.RecordRelay(success, channelID, duration)
	statsLogger := log.WithContext(ctx)
	if statsErr := op.StatsTotalUpdate(globalStats); statsErr != nil {
		statsLogger.Warnw("failed to update total relay statistics", "error", statsErr)
	}
	if statsErr := op.StatsHourlyUpdate(globalStats); statsErr != nil {
		statsLogger.Warnw("failed to update hourly relay statistics", "error", statsErr)
	}
	if statsErr := op.StatsDailyUpdate(context.Background(), globalStats); statsErr != nil {
		statsLogger.Warnw("failed to update daily relay statistics", "error", statsErr)
	}
	if statsErr := op.StatsAPIKeyUpdate(m.APIKeyID, globalStats); statsErr != nil {
		statsLogger.Warnw("failed to update API key relay statistics", "api_key_id", m.APIKeyID, "error", statsErr)
	}
	if channelID > 0 {
		// 通道成功/失败和等待时间在每次 attempt 结束时已记录；这里仅把最终响应的用量成本归到实际通道，避免重复计数。
		if statsErr := op.StatsChannelUpdate(channelID, model.StatsMetrics{
			InputToken:     m.Stats.InputToken,
			OutputToken:    m.Stats.OutputToken,
			ReasoningToken: m.Stats.ReasoningToken,
			InputCost:      m.Stats.InputCost,
			OutputCost:     m.Stats.OutputCost,
		}); statsErr != nil {
			statsLogger.Warnw("failed to update channel relay statistics", "channel_id", channelID, "error", statsErr)
		}
		if finalKeyID > 0 {
			if statsErr := op.StatsChannelKeyUpdate(channelID, finalKeyID, model.StatsMetrics{
				InputToken:     m.Stats.InputToken,
				OutputToken:    m.Stats.OutputToken,
				ReasoningToken: m.Stats.ReasoningToken,
				InputCost:      m.Stats.InputCost,
				OutputCost:     m.Stats.OutputCost,
			}); statsErr != nil {
				statsLogger.Warnw("failed to update channel key relay statistics", "channel_id", channelID, "channel_key_id", finalKeyID, "error", statsErr)
			}
		}
	}

	log.WithContext(ctx).Infow("relay complete",
		"model", m.RequestModel,
		"channel_id", channelID,
		"channel_name", channelName,
		"final_key_id", finalKeyID,
		"final_status", finalStatus,
		"success", success,
		"duration_ms", duration.Milliseconds(),
		"input_token", m.Stats.InputToken,
		"output_token", m.Stats.OutputToken,
		"reasoning_token", m.Stats.ReasoningToken,
		"input_cost", m.Stats.InputCost,
		"output_cost", m.Stats.OutputCost,
		"total_cost", m.Stats.InputCost+m.Stats.OutputCost,
		"attempts", len(attempts),
	)

	// 输出 attempts 摘要（第一阶段可观测性增强）
	if len(attempts) > 0 {
		log.Infof("  attempts summary:")
		for _, a := range attempts {
			ftMsg := ""
			if a.FirstTokenTime > 0 {
				ftMsg = fmt.Sprintf(", first_token=%s", formatMillis(a.FirstTokenTime))
			}
			log.Infof("    #%d: channel=%s(%d), key=%d, status=%s, duration=%s, sticky=%t%s, msg=%s",
				a.AttemptNum, a.ChannelName, a.ChannelID, a.ChannelKeyID, a.Status,
				formatMillis(a.Duration), a.Sticky, ftMsg, a.Msg)
		}
	}

	// 客户端断开或请求上下文取消后仍要保存最终审计日志，因此持久化阶段主动脱离请求取消信号。
	m.saveLog(context.WithoutCancel(ctx), err, duration, attempts, channelID, channelName)
}

// formatMillis 格式化毫秒数为可读字符串
func formatMillis(ms int) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.2fs", float64(ms)/1000.0)
}

func finalAttempt(attempts []model.ChannelAttempt) (int, string, int, model.AttemptStatus) {
	// Prefer the last real upstream outcome over a later routing decision. A
	// failed request can be followed by skipped/circuit-broken candidates while
	// the iterator exhausts the group. Those bookkeeping events must not replace
	// the channel that actually produced the final error in the relay log.
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if isRealUpstreamAttempt(a.Status) {
			return a.ChannelID, a.ChannelName, a.ChannelKeyID, a.Status
		}
	}

	// If no request reached an upstream, retain the most recent routing
	// decision as the best available attribution (for example, a circuit break
	// or an unavailable channel).
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.ChannelID > 0 || a.ChannelName != "" || a.Status != "" {
			return a.ChannelID, a.ChannelName, a.ChannelKeyID, a.Status
		}
	}
	return 0, "", 0, ""
}

func isRealUpstreamAttempt(status model.AttemptStatus) bool {
	switch status {
	case model.AttemptSuccess, model.AttemptFailed, model.AttemptClientCancel:
		return true
	default:
		return false
	}
}

func (m *RelayMetrics) saveLog(ctx context.Context, err error, duration time.Duration, attempts []model.ChannelAttempt, channelID int, channelName string) {
	contentMode, modeErr := op.RelayLogContentModeGet()
	if modeErr != nil {
		log.Warnf("failed to resolve relay log content policy: %v", modeErr)
		return
	}
	if contentMode == model.RelayLogContentModeDisabled {
		return
	}

	relayLog := model.RelayLog{
		Time:             m.StartTime.Unix(),
		RequestModelName: m.RequestModel,
		ChannelName:      channelName,
		ChannelId:        channelID,
		ActualModelName:  m.ActualModel,
		UseTime:          int(duration.Milliseconds()),
		Attempts:         attempts,
		TotalAttempts:    len(attempts),
	}

	if apiKey, getErr := op.APIKeyGet(m.APIKeyID, ctx); getErr == nil {
		relayLog.RequestAPIKeyName = apiKey.Name
	}

	// 首字时间
	if !m.FirstTokenTime.IsZero() {
		relayLog.Ftut = int(m.FirstTokenTime.Sub(m.StartTime).Milliseconds())
	}

	// 用量
	if m.Stats.InputToken > 0 || m.Stats.OutputToken > 0 {
		relayLog.InputTokens = int(m.Stats.InputToken)
		relayLog.OutputTokens = int(m.Stats.OutputToken)
		relayLog.ReasoningTokens = int(m.Stats.ReasoningToken)
		relayLog.Cost = m.Stats.InputCost + m.Stats.OutputCost
	}

	if contentMode == model.RelayLogContentModeFull {
		relayLog.RequestContent = m.requestContent()
		if len(m.InternalResponse) > 0 {
			relayLog.ResponseContent = string(m.InternalResponse)
		}
	}
	if err != nil {
		relayLog.Error = err.Error()
	}

	if logErr := op.RelayLogAdd(ctx, relayLog); logErr != nil {
		log.Warnf("failed to save relay log: %v", logErr)
	}
}

func (m *RelayMetrics) requestContent() string {
	if m.InternalRequest == nil {
		return ""
	}

	reqJSON, err := json.Marshal(filterRequestForLog(m.InternalRequest))
	if err != nil {
		return ""
	}

	var reqMap map[string]any
	if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
		// 解析失败时退回最朴素的可审计内容；若有出站摘要则无法并入，但保留原始请求体优先。
		return string(reqJSON)
	}

	if m.ParamOverride != "" {
		var override map[string]any
		if err := json.Unmarshal([]byte(m.ParamOverride), &override); err == nil {
			// 日志里的请求体要反映本次实际发给上游的参数覆盖。
			maps.Copy(reqMap, override)
		}
	}

	// raw passthrough 实际发往上游的是 patch 后的原始字节，与标准化 llm.Request 不同。
	// 这里并入出站摘要（model/stream/stream_options/字节数/sha256），让审计能核对真实出站语义，
	// 但不落库完整 raw body，避免未知字段与敏感内容全文入库。
	if m.OutboundRequestSummary != nil {
		reqMap["_outbound_request"] = m.OutboundRequestSummary
	}

	finalJSON, err := json.Marshal(reqMap)
	if err != nil {
		return string(reqJSON)
	}
	return string(finalJSON)
}

// filterRequestForLog 去掉 RawRequest、附件正文和可携带凭据的附件 URL，
// 避免 multipart 原始 body 或 JSON 多模态内容落库。
func filterRequestForLog(req *llm.Request) *llm.Request {
	if req == nil {
		return nil
	}
	filtered := *req
	filtered.RawRequest = nil
	if len(req.Messages) > 0 {
		filtered.Messages = append([]llm.Message(nil), req.Messages...)
		for i := range filtered.Messages {
			message := &filtered.Messages[i]
			if len(message.Content.MultipleContent) == 0 {
				continue
			}
			message.Content.MultipleContent = append([]llm.MessageContentPart(nil), message.Content.MultipleContent...)
			for j := range message.Content.MultipleContent {
				part := &message.Content.MultipleContent[j]
				switch part.Type {
				case "image_url":
					if part.ImageURL != nil {
						image := *part.ImageURL
						image.URL = "[redacted attachment]"
						part.ImageURL = &image
					}
				case "video_url":
					if part.VideoURL != nil {
						video := *part.VideoURL
						video.URL = "[redacted attachment]"
						part.VideoURL = &video
					}
				case "document":
					if part.Document != nil {
						document := *part.Document
						document.URL = "[redacted attachment]"
						part.Document = &document
					}
				case "input_audio":
					if part.InputAudio != nil {
						audio := *part.InputAudio
						audio.Data = "[redacted attachment]"
						part.InputAudio = &audio
					}
				}
			}
		}
	}
	if req.Image != nil {
		img := *req.Image
		if len(img.Images) > 0 {
			img.Images = nil
		}
		if len(img.Mask) > 0 {
			img.Mask = nil
		}
		filtered.Image = &img
	}
	return &filtered
}
