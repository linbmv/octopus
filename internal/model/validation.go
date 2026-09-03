package model

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/requestrewrite"
	"github.com/looplj/axonhub/llm"
)

const (
	MaxResourceNameBytes     = 64
	MaxModelNameBytes        = 256
	MaxBaseURLBytes          = 4096
	MaxBaseURLs              = 16
	MaxChannelKeys           = 64
	MaxGroupItems            = 1024
	MaxChannelKeyBytes       = 8192
	MaxCustomHeaders         = 64
	MaxHeaderRules           = 32
	MaxJSONRewriteRules      = 32
	MaxHeaderNameBytes       = 128
	MaxHeaderValueBytes      = 8192
	MaxParamOverrideBytes    = 64 << 10
	MaxJSONRewriteValueBytes = 16 << 10
	MaxRewriteRulesBytes     = 64 << 10
	MaxJSONRewriteValueDepth = 16
	MaxJSONRewriteValueNodes = 1024
	MaxModelListBytes        = 64 << 10
	MaxGroupTimeoutSeconds   = 24 * 60 * 60
	MaxSessionKeepSeconds    = 30 * 24 * 60 * 60
	// HardMaxInitialResponseTimeoutSeconds is the default non-disableable
	// initial-response ceiling. A channel may exceed it only through the
	// explicit, validated exception fields on Channel.
	HardMaxInitialResponseTimeoutSeconds        = 120
	MaxChannelFirstTokenTimeoutExceptionSeconds = 600
	MaxGroupItemPriority                        = 1_000_000
	MaxGroupItemWeight                          = 1_000_000
	MaxAPIKeyCost                               = 1_000_000_000_000
	MaxLLMPrice                                 = 1_000_000_000
	MaxUnixTimestamp                            = int64(253402300799) // 9999-12-31T23:59:59Z
	maxSupportedModelsPerKey                    = 256
)

var supportedChannelTypes = map[llm.APIFormat]struct{}{
	llm.APIFormatOpenAIChatCompletion:  {},
	llm.APIFormatOpenAIResponse:        {},
	llm.APIFormatOpenAIResponseCompact: {},
	llm.APIFormatOpenAIEmbedding:       {},
	llm.APIFormatOpenAIImageGeneration: {},
	llm.APIFormatOpenAIImageEdit:       {},
	llm.APIFormatOpenAIImageVariation:  {},
	llm.APIFormatAnthropicMessage:      {},
	llm.APIFormatGeminiContents:        {},
	ChannelTypeDoubao:                  {},
}

// ValidateChannel normalizes and validates a complete channel before it is
// persisted. It deliberately accepts disabled keys, but requires at least one
// non-empty key so a channel cannot be created in an unusable shape by mistake.
func ValidateChannel(channel *Channel) error {
	if channel == nil {
		return fmt.Errorf("channel is required")
	}
	channel.Name = strings.TrimSpace(channel.Name)
	if err := validateRequiredText("channel name", channel.Name, MaxResourceNameBytes); err != nil {
		return err
	}
	if err := ValidateChannelType(channel.Type); err != nil {
		return err
	}
	if err := validateBaseURLs(channel.BaseUrls); err != nil {
		return err
	}
	if len(channel.Keys) == 0 || len(channel.Keys) > MaxChannelKeys {
		return fmt.Errorf("channel keys must contain between 1 and %d entries", MaxChannelKeys)
	}
	for i := range channel.Keys {
		channel.Keys[i].ChannelKey = strings.TrimSpace(channel.Keys[i].ChannelKey)
		if err := validateChannelKeyEnvelope(fmt.Sprintf("channel key %d", i), channel.Keys[i].ChannelKey); err != nil {
			return err
		}
		if err := validateOptionalText(fmt.Sprintf("channel key remark %d", i), channel.Keys[i].Remark, MaxHeaderValueBytes); err != nil {
			return err
		}
	}
	channel.Model = strings.TrimSpace(channel.Model)
	channel.CustomModel = strings.TrimSpace(channel.CustomModel)
	channel.UserAgent = strings.TrimSpace(channel.UserAgent)
	if channel.PolicyProfile == "" {
		channel.PolicyProfile = ChannelPolicyStandard
	}
	if channel.ConfigVersion <= 0 {
		channel.ConfigVersion = 1
	}
	if err := validateChannelFirstTokenTimeoutException(channel.FirstTokenTimeoutExceptionEnabled, channel.FirstTokenTimeoutExceptionSeconds); err != nil {
		return err
	}
	if !channel.PolicyProfile.Valid() {
		return fmt.Errorf("invalid channel policy_profile %q", channel.PolicyProfile)
	}
	return validateChannelFields(channel.AutoGroup, channel.CustomHeader, channel.HeaderRules, channel.JSONRewriteRules, channel.ChannelProxy, channel.ParamOverride,
		channel.RPMLimit, channel.MaxConcurrency, channel.Model, channel.CustomModel, channel.UserAgent)
}

// ValidateChannelKeyUniqueness rejects duplicate credentials in one channel.
// Credentials are compared after the same whitespace normalization used by
// ValidateChannel, so two values that would be persisted identically cannot
// be added as separate routing candidates.
func ValidateChannelKeyUniqueness(keys []ChannelKey) error {
	seen := make(map[string]int, len(keys))
	for i, key := range keys {
		value := strings.TrimSpace(key.ChannelKey)
		if value == "" {
			continue
		}
		if previous, ok := seen[value]; ok {
			return fmt.Errorf("channel key %d duplicates channel key %d", i, previous)
		}
		seen[value] = i
	}
	return nil
}

// ValidateChannelUpdate validates fields supplied by a partial channel update.
func ValidateChannelUpdate(req *ChannelUpdateRequest) error {
	if req == nil || req.ID <= 0 {
		return fmt.Errorf("channel id must be positive")
	}
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if err := validateRequiredText("channel name", *req.Name, MaxResourceNameBytes); err != nil {
			return err
		}
	}
	if req.Type != nil {
		if err := ValidateChannelType(*req.Type); err != nil {
			return err
		}
	}
	if req.PolicyProfile != nil && !req.PolicyProfile.Valid() {
		return fmt.Errorf("invalid channel policy_profile %q", *req.PolicyProfile)
	}
	if req.FirstTokenTimeoutExceptionEnabled != nil && *req.FirstTokenTimeoutExceptionEnabled {
		if req.FirstTokenTimeoutExceptionSeconds == nil || *req.FirstTokenTimeoutExceptionSeconds <= HardMaxInitialResponseTimeoutSeconds {
			return fmt.Errorf("channel first-token timeout exception must be greater than %d seconds when enabled", HardMaxInitialResponseTimeoutSeconds)
		}
	}
	if req.FirstTokenTimeoutExceptionSeconds != nil {
		seconds := *req.FirstTokenTimeoutExceptionSeconds
		if seconds < 0 || seconds > MaxChannelFirstTokenTimeoutExceptionSeconds {
			return fmt.Errorf("channel first-token timeout exception must be between 0 and %d seconds", MaxChannelFirstTokenTimeoutExceptionSeconds)
		}
		if seconds != 0 && seconds <= HardMaxInitialResponseTimeoutSeconds {
			return fmt.Errorf("channel first-token timeout exception must be 0 or greater than %d seconds", HardMaxInitialResponseTimeoutSeconds)
		}
	}
	if req.BaseUrls != nil {
		if err := validateBaseURLs(*req.BaseUrls); err != nil {
			return err
		}
	}
	trimOptionalStringPointer(req.Model)
	trimOptionalStringPointer(req.CustomModel)
	trimOptionalStringPointer(req.UserAgent)
	autoGroup := AutoGroupTypeNone
	if req.AutoGroup != nil {
		autoGroup = *req.AutoGroup
	}
	if err := validateChannelFields(autoGroup, derefHeaders(req.CustomHeader), derefHeaderRules(req.HeaderRules), derefJSONRewriteRules(req.JSONRewriteRules), req.ChannelProxy, req.ParamOverride,
		derefInt(req.RPMLimit), derefInt(req.MaxConcurrency), derefString(req.Model), derefString(req.CustomModel), derefString(req.UserAgent)); err != nil {
		return err
	}
	if len(req.KeysToAdd) > MaxChannelKeys || len(req.KeysToUpdate) > MaxChannelKeys || len(req.KeysToDelete) > MaxChannelKeys {
		return fmt.Errorf("channel key changes may contain at most %d entries per operation", MaxChannelKeys)
	}
	for i := range req.KeysToAdd {
		req.KeysToAdd[i].ChannelKey = strings.TrimSpace(req.KeysToAdd[i].ChannelKey)
		if err := validateChannelKeyEnvelope(fmt.Sprintf("new channel key %d", i), req.KeysToAdd[i].ChannelKey); err != nil {
			return err
		}
		if err := validateOptionalText(fmt.Sprintf("new channel key remark %d", i), req.KeysToAdd[i].Remark, MaxHeaderValueBytes); err != nil {
			return err
		}
	}
	changedIDs := make(map[int]string, len(req.KeysToUpdate)+len(req.KeysToDelete))
	for i := range req.KeysToUpdate {
		key := &req.KeysToUpdate[i]
		if key.ID <= 0 {
			return fmt.Errorf("channel key update id must be positive")
		}
		if previous, ok := changedIDs[key.ID]; ok {
			return fmt.Errorf("channel key %d appears in both %s and update operations", key.ID, previous)
		}
		changedIDs[key.ID] = "update"
		if key.ChannelKey != nil {
			*key.ChannelKey = strings.TrimSpace(*key.ChannelKey)
			if err := validateChannelKeyEnvelope(fmt.Sprintf("channel key %d", key.ID), *key.ChannelKey); err != nil {
				return err
			}
		}
		if key.Remark != nil {
			if err := validateOptionalText(fmt.Sprintf("channel key remark %d", key.ID), *key.Remark, MaxHeaderValueBytes); err != nil {
				return err
			}
		}
	}
	for _, id := range req.KeysToDelete {
		if id <= 0 {
			return fmt.Errorf("channel key delete id must be positive")
		}
		if previous, ok := changedIDs[id]; ok {
			return fmt.Errorf("channel key %d appears in both %s and delete operations", id, previous)
		}
		changedIDs[id] = "delete"
	}
	return nil
}

func validateChannelFirstTokenTimeoutException(enabled bool, seconds int) error {
	if seconds < 0 || seconds > MaxChannelFirstTokenTimeoutExceptionSeconds {
		return fmt.Errorf("channel first-token timeout exception must be between 0 and %d seconds", MaxChannelFirstTokenTimeoutExceptionSeconds)
	}
	if seconds != 0 && seconds <= HardMaxInitialResponseTimeoutSeconds {
		return fmt.Errorf("channel first-token timeout exception must be 0 or greater than %d seconds", HardMaxInitialResponseTimeoutSeconds)
	}
	if enabled && seconds == 0 {
		return fmt.Errorf("channel first-token timeout exception must be greater than %d seconds when enabled", HardMaxInitialResponseTimeoutSeconds)
	}
	return nil
}

func ValidateChannelType(channelType llm.APIFormat) error {
	if _, ok := supportedChannelTypes[channelType]; !ok {
		return fmt.Errorf("unsupported channel type %q", channelType)
	}
	return nil
}

func validateChannelKeyEnvelope(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > MaxChannelKeyBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, MaxChannelKeyBytes)
	}
	if !strings.ContainsAny(value, "\r\n\t") {
		return nil
	}
	return fmt.Errorf("%s contains a forbidden control character", field)
}

func validateChannelFields(autoGroup AutoGroupType, headers []CustomHeader, headerRules []HeaderRule, jsonRules []JSONRewriteRule, proxyURL, paramOverride *string, rpmLimit, maxConcurrency int, modelNames, customModels, userAgent string) error {
	if autoGroup < AutoGroupTypeNone || autoGroup > AutoGroupTypeRegex {
		return fmt.Errorf("auto_group must be between %d and %d", AutoGroupTypeNone, AutoGroupTypeRegex)
	}
	if rpmLimit < 0 || maxConcurrency < 0 {
		return fmt.Errorf("rpm_limit and max_concurrency must be non-negative")
	}
	if err := validateOptionalText("model", modelNames, MaxModelListBytes); err != nil {
		return err
	}
	if err := validateOptionalText("custom_model", customModels, MaxModelListBytes); err != nil {
		return err
	}
	if err := validateOptionalText("user_agent", userAgent, MaxHeaderValueBytes); err != nil {
		return err
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return fmt.Errorf("user_agent must not contain line breaks")
	}
	if err := validateCustomHeaders(headers); err != nil {
		return err
	}
	if err := validateHeaderRules(headerRules); err != nil {
		return err
	}
	if err := validateJSONRewriteRules(jsonRules); err != nil {
		return err
	}
	if proxyURL != nil {
		trimmed := strings.TrimSpace(*proxyURL)
		*proxyURL = trimmed
		if trimmed != "" {
			parsed, err := url.Parse(trimmed)
			if err != nil || parsed.Host == "" {
				return fmt.Errorf("channel_proxy must be a valid proxy URL")
			}
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https", "socks", "socks5":
			default:
				return fmt.Errorf("channel_proxy scheme must be http, https, socks, or socks5")
			}
		}
	}
	if paramOverride != nil {
		trimmed := strings.TrimSpace(*paramOverride)
		*paramOverride = trimmed
		if trimmed != "" {
			if len(trimmed) > MaxParamOverrideBytes {
				return fmt.Errorf("param_override exceeds %d bytes", MaxParamOverrideBytes)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
				return fmt.Errorf("param_override must be a JSON object")
			}
		}
	}
	return nil
}

func validateBaseURLs(baseURLs []BaseUrl) error {
	if len(baseURLs) == 0 || len(baseURLs) > MaxBaseURLs {
		return fmt.Errorf("base_urls must contain between 1 and %d entries", MaxBaseURLs)
	}
	seen := make(map[string]struct{}, len(baseURLs))
	for i := range baseURLs {
		baseURLs[i].URL = strings.TrimSpace(baseURLs[i].URL)
		if err := validateRequiredText(fmt.Sprintf("base URL %d", i), baseURLs[i].URL, MaxBaseURLBytes); err != nil {
			return err
		}
		parsed, err := url.Parse(baseURLs[i].URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("base URL %d must be an http(s) URL without credentials or fragment", i)
		}
		if baseURLs[i].Delay < 0 {
			return fmt.Errorf("base URL %d delay must be non-negative", i)
		}
		canonical := strings.ToLower(parsed.Scheme+"://"+parsed.Host) + parsed.EscapedPath()
		if _, ok := seen[canonical]; ok {
			return fmt.Errorf("base URL %d is duplicated", i)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func validateCustomHeaders(headers []CustomHeader) error {
	if len(headers) > MaxCustomHeaders {
		return fmt.Errorf("custom_header may contain at most %d entries", MaxCustomHeaders)
	}
	seen := make(map[string]struct{}, len(headers))
	for i := range headers {
		headers[i].HeaderKey = strings.TrimSpace(headers[i].HeaderKey)
		if err := validateRequiredText(fmt.Sprintf("custom header %d name", i), headers[i].HeaderKey, MaxHeaderNameBytes); err != nil {
			return err
		}
		for _, r := range headers[i].HeaderKey {
			if !isHTTPTokenRune(r) {
				return fmt.Errorf("custom header %d has an invalid name", i)
			}
		}
		if err := validateOptionalText(fmt.Sprintf("custom header %d value", i), headers[i].HeaderValue, MaxHeaderValueBytes); err != nil {
			return err
		}
		if strings.ContainsAny(headers[i].HeaderValue, "\r\n") {
			return fmt.Errorf("custom header %d value must not contain line breaks", i)
		}
		key := strings.ToLower(headers[i].HeaderKey)
		if requestrewrite.IsProtectedHeader(key) {
			return fmt.Errorf("custom header %q is an authentication header and cannot be rewritten", headers[i].HeaderKey)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("custom header %q is duplicated", headers[i].HeaderKey)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateHeaderRules(rules []HeaderRule) error {
	if len(rules) > MaxHeaderRules {
		return fmt.Errorf("header_rules may contain at most %d entries", MaxHeaderRules)
	}
	totalBytes := 0
	for i := range rules {
		rules[i].Action = strings.ToLower(strings.TrimSpace(rules[i].Action))
		rules[i].HeaderKey = strings.TrimSpace(rules[i].HeaderKey)
		switch rules[i].Action {
		case "set", "append", "remove":
		default:
			return fmt.Errorf("header rule %d action must be set, append, or remove", i)
		}
		if err := validateRequiredText(fmt.Sprintf("header rule %d name", i), rules[i].HeaderKey, MaxHeaderNameBytes); err != nil {
			return err
		}
		for _, r := range rules[i].HeaderKey {
			if !isHTTPTokenRune(r) {
				return fmt.Errorf("header rule %d has an invalid name", i)
			}
		}
		if requestrewrite.IsProtectedHeader(rules[i].HeaderKey) {
			return fmt.Errorf("header rule %d targets protected authentication header %q", i, rules[i].HeaderKey)
		}
		if rules[i].Action == "remove" {
			rules[i].HeaderValue = ""
		} else {
			if err := validateOptionalText(fmt.Sprintf("header rule %d value", i), rules[i].HeaderValue, MaxHeaderValueBytes); err != nil {
				return err
			}
			if strings.ContainsAny(rules[i].HeaderValue, "\r\n") {
				return fmt.Errorf("header rule %d value must not contain line breaks", i)
			}
			if rules[i].Action == "append" && rules[i].HeaderValue == "" {
				return fmt.Errorf("header rule %d append value must not be empty", i)
			}
		}
		totalBytes += len(rules[i].Action) + len(rules[i].HeaderKey) + len(rules[i].HeaderValue)
		if totalBytes > MaxRewriteRulesBytes {
			return fmt.Errorf("header_rules exceed %d total bytes", MaxRewriteRulesBytes)
		}
	}
	return nil
}

func validateJSONRewriteRules(rules []JSONRewriteRule) error {
	if len(rules) > MaxJSONRewriteRules {
		return fmt.Errorf("json_rewrite_rules may contain at most %d entries", MaxJSONRewriteRules)
	}
	totalBytes := 0
	for i := range rules {
		rules[i].Action = strings.ToLower(strings.TrimSpace(rules[i].Action))
		rules[i].Path = strings.TrimSpace(rules[i].Path)
		if _, err := requestrewrite.ParseJSONPointer(rules[i].Path); err != nil {
			return fmt.Errorf("json rewrite rule %d path: %w", i, err)
		}
		switch rules[i].Action {
		case "remove":
			rules[i].Value = nil
		case "override":
			if rules[i].Value == nil {
				return fmt.Errorf("json rewrite rule %d override value is required", i)
			}
			trimmed := strings.TrimSpace(*rules[i].Value)
			*rules[i].Value = trimmed
			if len(trimmed) == 0 || len(trimmed) > MaxJSONRewriteValueBytes || !json.Valid([]byte(trimmed)) {
				return fmt.Errorf("json rewrite rule %d value must be one valid JSON value of at most %d bytes", i, MaxJSONRewriteValueBytes)
			}
			var value any
			if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
				return fmt.Errorf("json rewrite rule %d value is invalid: %w", i, err)
			}
			nodes := 0
			if err := validateJSONRewriteValueShape(value, 1, &nodes); err != nil {
				return fmt.Errorf("json rewrite rule %d value: %w", i, err)
			}
		default:
			return fmt.Errorf("json rewrite rule %d action must be override or remove", i)
		}
		valueBytes := 0
		if rules[i].Value != nil {
			valueBytes = len(*rules[i].Value)
		}
		totalBytes += len(rules[i].Action) + len(rules[i].Path) + valueBytes
		if totalBytes > MaxRewriteRulesBytes {
			return fmt.Errorf("json_rewrite_rules exceed %d total bytes", MaxRewriteRulesBytes)
		}
	}
	return nil
}

func validateJSONRewriteValueShape(value any, depth int, nodes *int) error {
	*nodes = *nodes + 1
	if *nodes > MaxJSONRewriteValueNodes {
		return fmt.Errorf("may contain at most %d values", MaxJSONRewriteValueNodes)
	}
	if depth > MaxJSONRewriteValueDepth {
		return fmt.Errorf("may be nested at most %d levels", MaxJSONRewriteValueDepth)
	}
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := validateJSONRewriteValueShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, child := range typed {
			if err := validateJSONRewriteValueShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

func isHTTPTokenRune(r rune) bool {
	if r > 127 || r <= 32 || r == 127 {
		return false
	}
	return !strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r)
}

// ValidateGroup normalizes and validates a complete group. Database-backed
// reference and cycle checks remain in the operation layer.
func ValidateGroup(group *Group) error {
	if group == nil {
		return fmt.Errorf("group is required")
	}
	group.Name = strings.TrimSpace(group.Name)
	if err := validateRequiredText("group name", group.Name, MaxResourceNameBytes); err != nil {
		return err
	}
	if err := validateGroupMode(group.Mode); err != nil {
		return err
	}
	if err := validateGroupDurations(group.FirstTokenTimeOut, group.SessionKeepTime); err != nil {
		return err
	}
	if len(group.Items) > MaxGroupItems {
		return fmt.Errorf("group may contain at most %d items", MaxGroupItems)
	}
	for i := range group.Items {
		group.Items[i].Type = normalizeValidationGroupItemType(group.Items[i].Type)
		group.Items[i].ModelName = strings.TrimSpace(group.Items[i].ModelName)
		if group.Items[i].Weight == 0 {
			group.Items[i].Weight = 1
		}
		if err := validateGroupItemShape(group.Items[i].Type, group.Items[i].ChannelID, group.Items[i].TargetGroupID, group.Items[i].ModelName, group.Items[i].Priority, group.Items[i].Weight); err != nil {
			return fmt.Errorf("group item %d: %w", i, err)
		}
	}
	return nil
}

func ValidateGroupUpdate(req *GroupUpdateRequest) error {
	if req == nil || req.ID <= 0 {
		return fmt.Errorf("group id must be positive")
	}
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if err := validateRequiredText("group name", *req.Name, MaxResourceNameBytes); err != nil {
			return err
		}
	}
	if req.Mode != nil {
		if err := validateGroupMode(*req.Mode); err != nil {
			return err
		}
	}
	if err := validateGroupDurations(derefInt(req.FirstTokenTimeOut), derefInt(req.SessionKeepTime)); err != nil {
		return err
	}
	if len(req.ItemsToAdd) > MaxGroupItems || len(req.ItemsToUpdate) > MaxGroupItems || len(req.ItemsToDelete) > MaxGroupItems {
		return fmt.Errorf("group item changes may contain at most %d entries per operation", MaxGroupItems)
	}
	for i := range req.ItemsToAdd {
		item := &req.ItemsToAdd[i]
		item.Type = normalizeValidationGroupItemType(item.Type)
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.Weight == 0 {
			item.Weight = 1
		}
		if err := validateGroupItemShape(item.Type, item.ChannelID, item.TargetGroupID, item.ModelName, item.Priority, item.Weight); err != nil {
			return fmt.Errorf("new group item %d: %w", i, err)
		}
	}
	changedIDs := make(map[int]string, len(req.ItemsToUpdate)+len(req.ItemsToDelete))
	for _, item := range req.ItemsToUpdate {
		if item.ID <= 0 {
			return fmt.Errorf("group item update id must be positive")
		}
		if item.Priority != nil && (*item.Priority < 0 || *item.Priority > MaxGroupItemPriority) {
			return fmt.Errorf("group item priority must be between 0 and %d", MaxGroupItemPriority)
		}
		if item.Weight != nil && (*item.Weight < 1 || *item.Weight > MaxGroupItemWeight) {
			return fmt.Errorf("group item weight must be between 1 and %d", MaxGroupItemWeight)
		}
		if item.Priority == nil && item.Weight == nil && item.Disabled == nil {
			return fmt.Errorf("group item update %d contains no changes", item.ID)
		}
		if _, ok := changedIDs[item.ID]; ok {
			return fmt.Errorf("group item %d appears more than once", item.ID)
		}
		changedIDs[item.ID] = "update"
	}
	for _, id := range req.ItemsToDelete {
		if id <= 0 {
			return fmt.Errorf("group item delete id must be positive")
		}
		if previous, ok := changedIDs[id]; ok {
			return fmt.Errorf("group item %d appears in both %s and delete operations", id, previous)
		}
		changedIDs[id] = "delete"
	}
	return nil
}

func validateGroupMode(mode GroupMode) error {
	if mode < GroupModeRoundRobin || mode > GroupModeWeighted {
		return fmt.Errorf("group mode must be between %d and %d", GroupModeRoundRobin, GroupModeWeighted)
	}
	return nil
}

func validateGroupDurations(firstTokenTimeout, sessionKeep int) error {
	if firstTokenTimeout < 0 || firstTokenTimeout > MaxGroupTimeoutSeconds {
		return fmt.Errorf("first_token_time_out must be between 0 and %d seconds", MaxGroupTimeoutSeconds)
	}
	if sessionKeep < 0 || sessionKeep > MaxSessionKeepSeconds {
		return fmt.Errorf("session_keep_time must be between 0 and %d seconds", MaxSessionKeepSeconds)
	}
	return nil
}

func validateGroupItemShape(itemType string, channelID, targetGroupID int, modelName string, priority, weight int) error {
	if priority < 0 || priority > MaxGroupItemPriority {
		return fmt.Errorf("priority must be between 0 and %d", MaxGroupItemPriority)
	}
	if weight < 1 || weight > MaxGroupItemWeight {
		return fmt.Errorf("weight must be between 1 and %d", MaxGroupItemWeight)
	}
	switch itemType {
	case GroupItemTypeChannel:
		if channelID <= 0 || targetGroupID != 0 {
			return fmt.Errorf("channel item requires a positive channel_id and no target_group_id")
		}
		return validateRequiredText("model_name", modelName, MaxModelNameBytes)
	case GroupItemTypeGroup:
		if targetGroupID <= 0 || channelID != 0 || modelName != "" {
			return fmt.Errorf("group item requires a positive target_group_id and no channel/model fields")
		}
		return nil
	default:
		return fmt.Errorf("type must be %q or %q", GroupItemTypeChannel, GroupItemTypeGroup)
	}
}

func normalizeValidationGroupItemType(itemType string) string {
	itemType = strings.ToLower(strings.TrimSpace(itemType))
	if itemType == "" {
		return GroupItemTypeChannel
	}
	return itemType
}

// ValidateAPIKey normalizes and validates administrator-controlled API key
// metadata. The generated secret itself is validated separately by the create
// path and by backup import.
func ValidateAPIKey(key *APIKey) error {
	if key == nil {
		return fmt.Errorf("API key is required")
	}
	key.Name = strings.TrimSpace(key.Name)
	if err := validateRequiredText("API key name", key.Name, MaxResourceNameBytes); err != nil {
		return err
	}
	if key.ExpireAt < 0 || key.ExpireAt > MaxUnixTimestamp {
		return fmt.Errorf("expire_at must be 0 or a valid Unix timestamp")
	}
	if math.IsNaN(key.MaxCost) || math.IsInf(key.MaxCost, 0) || key.MaxCost < 0 || key.MaxCost > MaxAPIKeyCost {
		return fmt.Errorf("max_cost must be a finite number between 0 and %g", float64(MaxAPIKeyCost))
	}
	normalized, err := normalizeSupportedModels(key.SupportedModels)
	if err != nil {
		return err
	}
	key.SupportedModels = normalized
	return nil
}

func normalizeSupportedModels(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if err := validateOptionalText("supported_models", value, MaxModelListBytes); err != nil {
		return "", err
	}
	parts := strings.Split(value, ",")
	if len(parts) > maxSupportedModelsPerKey {
		return "", fmt.Errorf("supported_models may contain at most %d models", maxSupportedModelsPerKey)
	}
	seen := make(map[string]struct{}, len(parts))
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if err := validateRequiredText("supported model", name, MaxModelNameBytes); err != nil {
			return "", err
		}
		canonical := strings.ToLower(name)
		if _, ok := seen[canonical]; ok {
			return "", fmt.Errorf("supported model %q is duplicated", name)
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, name)
	}
	return strings.Join(normalized, ","), nil
}

func ValidateLLMInfo(info *LLMInfo) error {
	if info == nil {
		return fmt.Errorf("LLM info is required")
	}
	info.Name = strings.ToLower(strings.TrimSpace(info.Name))
	if err := validateRequiredText("model name", info.Name, MaxModelNameBytes); err != nil {
		return err
	}
	if strings.Contains(info.Name, ",") {
		return fmt.Errorf("model name must not contain a comma")
	}
	prices := map[string]float64{
		"input":       info.Input,
		"output":      info.Output,
		"cache_read":  info.CacheRead,
		"cache_write": info.CacheWrite,
	}
	names := make([]string, 0, len(prices))
	for name := range prices {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := prices[name]
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > MaxLLMPrice {
			return fmt.Errorf("%s price must be a finite number between 0 and %g", name, float64(MaxLLMPrice))
		}
	}
	return nil
}

func validateRequiredText(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	for _, r := range value {
		if r == 0 || r == '\r' || r == '\n' {
			return fmt.Errorf("%s contains a forbidden control character", field)
		}
	}
	return nil
}

func validateOptionalText(field, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	return validateRequiredText(field, value, maxBytes)
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefHeaders(value *[]CustomHeader) []CustomHeader {
	if value == nil {
		return nil
	}
	return *value
}

func derefHeaderRules(value *[]HeaderRule) []HeaderRule {
	if value == nil {
		return nil
	}
	return *value
}

func derefJSONRewriteRules(value *[]JSONRewriteRule) []JSONRewriteRule {
	if value == nil {
		return nil
	}
	return *value
}

func trimOptionalStringPointer(value *string) {
	if value != nil {
		*value = strings.TrimSpace(*value)
	}
}
