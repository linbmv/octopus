package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm"
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
}

type OutboundRequestSummary struct {
	RawPassthrough       bool           `json:"raw_passthrough"`
	ParamOverrideApplied bool           `json:"param_override_applied,omitempty"`
	BodyBytes            int            `json:"body_bytes"`
	BodySHA256           string         `json:"body_sha256"`
	Model                string         `json:"model,omitempty"`
	Stream               *bool          `json:"stream,omitempty"`
	StreamOptions        map[string]any `json:"stream_options,omitempty"`
}

func (m *RelayMetrics) RecordUsage(usage *llm.Usage) {
	if usage == nil {
		return
	}

	// usage 已由 axonhub/llm 标准化；octopus 仍使用本地模型价格表计算成本，所以这里只做用量落点和价格换算。
	m.Stats.InputToken = usage.PromptTokens
	m.Stats.OutputToken = usage.CompletionTokens

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

func (m *RelayMetrics) RecordOutboundRequestSummary(body []byte, rawPassthrough bool, paramOverrideApplied bool) {
	sum := sha256.Sum256(body)
	summary := &OutboundRequestSummary{
		RawPassthrough:       rawPassthrough,
		ParamOverrideApplied: paramOverrideApplied,
		BodyBytes:            len(body),
		BodySHA256:           hex.EncodeToString(sum[:]),
	}

	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err == nil {
		if model, ok := bodyMap["model"].(string); ok {
			summary.Model = model
		}
		if stream, ok := bodyMap["stream"].(bool); ok {
			summary.Stream = &stream
		}
		if streamOptions, ok := bodyMap["stream_options"].(map[string]any); ok {
			summary.StreamOptions = streamOptions
		}
	}

	m.OutboundRequestSummary = summary
}

func (m *RelayMetrics) Save(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt) {
	duration := time.Since(m.StartTime)

	globalStats := model.StatsMetrics{
		WaitTime:    duration.Milliseconds(),
		InputToken:  m.Stats.InputToken,
		OutputToken: m.Stats.OutputToken,
		InputCost:   m.Stats.InputCost,
		OutputCost:  m.Stats.OutputCost,
	}
	if success {
		globalStats.RequestSuccess = 1
	} else {
		globalStats.RequestFailed = 1
	}

	channelID, channelName, finalKeyID, finalStatus := finalAttempt(attempts)
	op.StatsTotalUpdate(globalStats)
	op.StatsHourlyUpdate(globalStats)
	op.StatsDailyUpdate(context.Background(), globalStats)
	op.StatsAPIKeyUpdate(m.APIKeyID, globalStats)
	if channelID > 0 {
		// 通道成功/失败和等待时间在每次 attempt 结束时已记录；这里仅把最终响应的用量成本归到实际通道，避免重复计数。
		op.StatsChannelUpdate(channelID, model.StatsMetrics{
			InputToken:  m.Stats.InputToken,
			OutputToken: m.Stats.OutputToken,
			InputCost:   m.Stats.InputCost,
			OutputCost:  m.Stats.OutputCost,
		})
	}

	log.Infof(
		"relay complete: model=%s, channel=%d(%s), final_key_id=%d, final_status=%s, "+
			"success=%t, duration=%dms, input_token=%d, output_token=%d, input_cost=%f, "+
			"output_cost=%f, total_cost=%f, attempts=%d",
		m.RequestModel, channelID, channelName, finalKeyID, finalStatus,
		success, duration.Milliseconds(), m.Stats.InputToken, m.Stats.OutputToken,
		m.Stats.InputCost, m.Stats.OutputCost, m.Stats.InputCost+m.Stats.OutputCost, len(attempts),
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
	var last model.ChannelAttempt
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Status == model.AttemptSuccess {
			return a.ChannelID, a.ChannelName, a.ChannelKeyID, a.Status
		}
		if last.Status == "" && (a.ChannelID > 0 || a.ChannelName != "" || a.Status != "") {
			last = a
		}
	}
	return last.ChannelID, last.ChannelName, last.ChannelKeyID, last.Status
}

func (m *RelayMetrics) saveLog(ctx context.Context, err error, duration time.Duration, attempts []model.ChannelAttempt, channelID int, channelName string) {
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
		relayLog.Cost = m.Stats.InputCost + m.Stats.OutputCost
	}

	relayLog.RequestContent = truncateLogContent(m.requestContent())
	if len(m.InternalResponse) > 0 {
		relayLog.ResponseContent = truncateLogContent(string(m.InternalResponse))
	}
	if err != nil {
		relayLog.Error = err.Error()
	}

	if logErr := op.RelayLogAdd(ctx, relayLog); logErr != nil {
		log.Warnf("failed to save relay log: %v", logErr)
	}
}

func truncateLogContent(content string) string {
	if len(content) <= conf.MaxRelayLogContentBytes {
		return content
	}
	return content[:conf.MaxRelayLogContentBytes] + "\n[truncated]"
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

// filterRequestForLog 去掉 RawRequest 和图片二进制字段，避免 multipart 原始 body 或图片内容落库。
func filterRequestForLog(req *llm.Request) *llm.Request {
	if req == nil {
		return nil
	}
	filtered := *req
	filtered.RawRequest = nil
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
