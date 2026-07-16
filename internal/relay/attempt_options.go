package relay

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"unicode"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
)

// applyChannelRequestOptions applies the channel's ordered, bounded outbound
// rewrite policy and records only a redacted audit summary of the changes.
func (ra *relayAttempt) applyChannelRequestOptions(outboundRequest *httpclient.Request) {
	// raw passthrough 必须先于 ParamOverride/CustomHeader：先把出站 body 还原为客户端原始字节，
	// 再让既有覆盖逻辑在其之上叠加，保持渠道配置语义不变。
	rawPassthroughApplied := ra.applyRawPassthrough(outboundRequest)
	paramOverrideApplied := false
	jsonRewriteApplied := false
	headerRewriteApplied := false
	// ParamOverride 只覆盖 JSON 请求体；multipart 图片编辑等请求不能按 map 合并。
	if ra.channel.ParamOverride != nil && *ra.channel.ParamOverride != "" && strings.Contains(strings.ToLower(outboundRequest.Headers.Get("Content-Type")+" "+outboundRequest.ContentType), "application/json") {
		var bodyMap map[string]any
		if err := json.Unmarshal(outboundRequest.Body, &bodyMap); err != nil {
			log.Warnf("failed to unmarshal request body: %v, skipping param_override", err)
		} else {
			var override map[string]any
			if err := json.Unmarshal([]byte(*ra.channel.ParamOverride), &override); err != nil {
				log.Warnf("failed to unmarshal param_override: %v, skipping", err)
			} else {
				maps.Copy(bodyMap, override)
				modifiedBody, err := json.Marshal(bodyMap)
				if err != nil {
					log.Warnf("failed to marshal modified body: %v, skipping param_override", err)
				} else {
					outboundRequest.Body = modifiedBody
					ra.metrics.ParamOverride = *ra.channel.ParamOverride
					paramOverrideApplied = true
				}
			}
		}
	}
	if len(ra.channel.JSONRewriteRules) > 0 && strings.Contains(strings.ToLower(outboundRequest.Headers.Get("Content-Type")+" "+outboundRequest.ContentType), "application/json") {
		modifiedBody, changed, err := applyJSONRewriteRules(outboundRequest.Body, ra.channel.JSONRewriteRules)
		if err != nil {
			log.Warnf("failed to apply JSON rewrite rules: %v", err)
		} else if changed {
			outboundRequest.Body = modifiedBody
			jsonRewriteApplied = true
		}
	}
	for _, header := range ra.channel.CustomHeader {
		if requestrewrite.IsProtectedHeader(header.HeaderKey) {
			continue
		}
		outboundRequest.Headers.Set(header.HeaderKey, header.HeaderValue)
		headerRewriteApplied = true
	}
	for _, rule := range ra.channel.HeaderRules {
		if requestrewrite.IsProtectedHeader(rule.HeaderKey) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "set":
			outboundRequest.Headers.Set(strings.TrimSpace(rule.HeaderKey), rule.HeaderValue)
			headerRewriteApplied = true
		case "append":
			outboundRequest.Headers.Add(strings.TrimSpace(rule.HeaderKey), rule.HeaderValue)
			headerRewriteApplied = true
		case "remove":
			outboundRequest.Headers.Del(strings.TrimSpace(rule.HeaderKey))
			headerRewriteApplied = true
		}
	}
	// 渠道 UA 字段优先级最高：在自定义头之后设置，作为该渠道出站的权威 User-Agent。
	// 供"仅特定客户端"的上游放行（如仅 Claude Code 客户端的中转站）。
	if ua := strings.TrimSpace(ra.channel.UserAgent); ua != "" {
		outboundRequest.Headers.Set("User-Agent", ua)
		headerRewriteApplied = true
	}
	// When outbound semantics differ from the normalized request, retain only a
	// bounded body hash/shape and boolean operation flags. Header values and body
	// content never enter this audit summary.
	if rawPassthroughApplied || paramOverrideApplied || jsonRewriteApplied || headerRewriteApplied {
		ra.metrics.RecordOutboundRequestSummary(
			outboundRequest.Body,
			rawPassthroughApplied,
			paramOverrideApplied,
			jsonRewriteApplied,
			headerRewriteApplied,
		)
	}
}

func applyJSONRewriteRules(body []byte, rules []dbmodel.JSONRewriteRule) ([]byte, bool, error) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return body, false, fmt.Errorf("decode request body: %w", err)
	}
	changed := false
	for i, rule := range rules {
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		tokens, err := requestrewrite.ParseJSONPointer(strings.TrimSpace(rule.Path))
		if err != nil {
			return body, false, fmt.Errorf("rule %d path: %w", i, err)
		}
		var value any
		if action == "override" {
			if rule.Value == nil {
				return body, false, fmt.Errorf("rule %d override value is required", i)
			}
			if err := json.Unmarshal([]byte(*rule.Value), &value); err != nil {
				return body, false, fmt.Errorf("rule %d value: %w", i, err)
			}
		}
		updated, applied, err := requestrewrite.ApplyJSONPointer(document, tokens, action, value)
		if err != nil {
			return body, false, fmt.Errorf("rule %d: %w", i, err)
		}
		if applied {
			document = updated
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	modified, err := json.Marshal(document)
	if err != nil {
		return body, false, fmt.Errorf("encode rewritten request body: %w", err)
	}
	return modified, true, nil
}

// cleanKeyRemark 清洗渠道 key 备注用于持久化：去除控制字符、trim、截断到 64 rune。
// 空备注返回空字符串，便于日志层用 omitempty 省略、前端按"仅显示备注"处理。
func cleanKeyRemark(remark string) string {
	remark = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, remark))
	runes := []rune(remark)
	if len(runes) > 64 {
		return string(runes[:64]) + "..."
	}
	return remark
}

// safeKeyRemark 在 cleanKeyRemark 基础上，为终端日志把空备注展示为 "-"。
func safeKeyRemark(remark string) string {
	if cleaned := cleanKeyRemark(remark); cleaned != "" {
		return cleaned
	}
	return "-"
}
