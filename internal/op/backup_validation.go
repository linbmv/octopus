package op

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/dlclark/regexp2"
	"github.com/google/uuid"
)

const (
	maxBackupNameBytes       = 256
	maxBackupSecretBytes     = 16 * 1024
	maxBackupURLBytes        = 8 * 1024
	maxBackupModelListBytes  = 1024 * 1024
	maxBackupJSONFieldBytes  = 1024 * 1024
	maxBackupRegexBytes      = 1024
	maxBackupHeaderBytes     = 16 * 1024
	maxBackupDurationSeconds = int64(^uint64(0)>>1) / int64(time.Second)
)

// DBImportValidationError identifies dump or target-state errors that are safe
// to expose as an HTTP 400. Database execution errors deliberately use ordinary
// wrapped errors so callers do not accidentally classify them as client input.
type DBImportValidationError struct {
	Field   string
	Problem string
}

func (e *DBImportValidationError) Error() string {
	if e == nil {
		return "invalid database dump"
	}
	if e.Field == "" {
		return "invalid database dump: " + e.Problem
	}
	return fmt.Sprintf("invalid database dump at %s: %s", e.Field, e.Problem)
}

func invalidDump(field, format string, args ...any) error {
	return &DBImportValidationError{Field: field, Problem: fmt.Sprintf(format, args...)}
}

type backupValidationState struct {
	channelIDs      map[int]model.Channel
	channelKeyIDs   map[int]model.ChannelKey
	channelModels   map[int]map[string]struct{}
	groupIDs        map[int]model.Group
	groupNames      map[string]struct{}
	apiKeyIDs       map[int]model.APIKey
	apiKeyNames     map[string]struct{}
	channelKeyOwner map[int]int
}

func validateDBDump(dump *model.DBDump) error {
	if dump == nil {
		return invalidDump("dump", "is required")
	}
	if dump.Version != dbDumpLegacyVersion && dump.Version != dbDumpVersion {
		return invalidDump("version", "unsupported version %d (expected %d or %d)", dump.Version, dbDumpLegacyVersion, dbDumpVersion)
	}
	if dump.ExportedAt.IsZero() {
		return invalidDump("exported_at", "is required")
	}
	if !dump.IncludeStats && hasDumpStats(dump) {
		return invalidDump("include_stats", "is false but statistics data is present")
	}
	if !dump.IncludeLogs && len(dump.RelayLogs) > 0 {
		return invalidDump("include_logs", "is false but relay_logs data is present")
	}

	state := &backupValidationState{
		channelIDs:      make(map[int]model.Channel, len(dump.Channels)),
		channelKeyIDs:   make(map[int]model.ChannelKey, len(dump.ChannelKeys)),
		channelModels:   make(map[int]map[string]struct{}, len(dump.Channels)),
		groupIDs:        make(map[int]model.Group, len(dump.Groups)),
		groupNames:      make(map[string]struct{}, len(dump.Groups)),
		apiKeyIDs:       make(map[int]model.APIKey, len(dump.APIKeys)),
		apiKeyNames:     make(map[string]struct{}, len(dump.APIKeys)),
		channelKeyOwner: make(map[int]int, len(dump.ChannelKeys)),
	}

	if err := validateDumpChannels(dump, state); err != nil {
		return err
	}
	if err := validateDumpGroups(dump, state); err != nil {
		return err
	}
	if err := validateDumpLLMInfos(dump.LLMInfos); err != nil {
		return err
	}
	if err := validateDumpAPIKeys(dump.APIKeys, state); err != nil {
		return err
	}
	if err := validateDumpSettings(dump.Settings); err != nil {
		return err
	}
	if err := validateDumpStats(dump, state); err != nil {
		return err
	}
	if err := validateDumpRelayLogs(dump.RelayLogs, state); err != nil {
		return err
	}
	if dump.Version == dbDumpVersion {
		return validateDumpV2Relations(dump)
	}
	return nil
}

func validateDumpV2Relations(dump *model.DBDump) error {
	if dump.Relations == nil {
		return invalidDump("relations", "is required for version 2")
	}
	channelUUIDs := make(map[string]int, len(dump.Channels))
	for i, channel := range dump.Channels {
		if err := collectDumpUUID(channelUUIDs, channel.UUID, channel.ID, fmt.Sprintf("channels[%d].uuid", i)); err != nil {
			return err
		}
	}
	groupUUIDs := make(map[string]int, len(dump.Groups))
	for i, group := range dump.Groups {
		if err := collectDumpUUID(groupUUIDs, group.UUID, group.ID, fmt.Sprintf("groups[%d].uuid", i)); err != nil {
			return err
		}
	}
	keyUUIDs := make(map[string]int, len(dump.ChannelKeys))
	for i, key := range dump.ChannelKeys {
		path := fmt.Sprintf("channel_keys[%d].uuid", i)
		if err := collectDumpUUID(keyUUIDs, key.UUID, key.ID, path); err != nil {
			return err
		}
		channelUUID, ok := dump.Relations.ChannelKeys[key.UUID]
		if !ok {
			return invalidDump("relations.channel_keys", "missing relationship for key UUID %s", key.UUID)
		}
		if channelUUIDs[channelUUID] != key.ChannelID {
			return invalidDump("relations.channel_keys", "key UUID %s does not reference channel id %d", key.UUID, key.ChannelID)
		}
	}
	itemUUIDs := make(map[string]int, len(dump.GroupItems))
	for i, item := range dump.GroupItems {
		path := fmt.Sprintf("group_items[%d].uuid", i)
		if err := collectDumpUUID(itemUUIDs, item.UUID, item.ID, path); err != nil {
			return err
		}
		relation, ok := dump.Relations.GroupItems[item.UUID]
		if !ok {
			return invalidDump("relations.group_items", "missing relationship for item UUID %s", item.UUID)
		}
		if groupUUIDs[relation.GroupUUID] != item.GroupID {
			return invalidDump("relations.group_items", "item UUID %s does not reference group id %d", item.UUID, item.GroupID)
		}
		if item.Type == model.GroupItemTypeChannel && channelUUIDs[relation.ChannelUUID] != item.ChannelID {
			return invalidDump("relations.group_items", "item UUID %s does not reference channel id %d", item.UUID, item.ChannelID)
		}
		if item.Type == model.GroupItemTypeGroup && groupUUIDs[relation.TargetGroupUUID] != item.TargetGroupID {
			return invalidDump("relations.group_items", "item UUID %s does not reference target group id %d", item.UUID, item.TargetGroupID)
		}
	}
	apiKeyUUIDs := make(map[string]int, len(dump.APIKeys))
	for i, key := range dump.APIKeys {
		if err := collectDumpUUID(apiKeyUUIDs, key.UUID, key.ID, fmt.Sprintf("api_keys[%d].uuid", i)); err != nil {
			return err
		}
	}
	return nil
}

func collectDumpUUID(seen map[string]int, value string, id int, path string) error {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return invalidDump(path, "must be a valid UUID")
	}
	canonical := parsed.String()
	if previous, exists := seen[canonical]; exists {
		return invalidDump(path, "duplicates UUID used by id %d", previous)
	}
	seen[canonical] = id
	return nil
}

func hasDumpStats(dump *model.DBDump) bool {
	return len(dump.StatsTotal) > 0 || len(dump.StatsDaily) > 0 || len(dump.StatsHourly) > 0 ||
		len(dump.StatsChannel) > 0 || len(dump.StatsAPIKey) > 0
}

func validateDumpChannels(dump *model.DBDump, state *backupValidationState) error {
	channelNames := make(map[string]struct{}, len(dump.Channels))
	for i, channel := range dump.Channels {
		path := fmt.Sprintf("channels[%d]", i)
		if channel.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := state.channelIDs[channel.ID]; exists {
			return invalidDump(path+".id", "duplicate channel id %d", channel.ID)
		}
		if err := validateRequiredBackupString(path+".name", channel.Name, 64); err != nil {
			return err
		}
		canonicalName := strings.ToLower(channel.Name)
		if _, exists := channelNames[canonicalName]; exists {
			return invalidDump(path+".name", "duplicate channel name %q", channel.Name)
		}
		channelNames[canonicalName] = struct{}{}
		if err := model.ValidateChannelType(channel.Type); err != nil {
			return invalidDump(path+".type", "%v", err)
		}
		if len(channel.BaseUrls) == 0 {
			return invalidDump(path+".base_urls", "must contain at least one URL")
		}
		seenURLs := make(map[string]struct{}, len(channel.BaseUrls))
		for j, baseURL := range channel.BaseUrls {
			urlPath := fmt.Sprintf("%s.base_urls[%d]", path, j)
			if err := validateHTTPURL(urlPath+".url", baseURL.URL); err != nil {
				return err
			}
			if _, exists := seenURLs[baseURL.URL]; exists {
				return invalidDump(urlPath+".url", "duplicate base URL %q", baseURL.URL)
			}
			seenURLs[baseURL.URL] = struct{}{}
			if baseURL.Delay < 0 {
				return invalidDump(urlPath+".delay", "must be non-negative")
			}
		}
		if channel.AutoGroup < model.AutoGroupTypeNone || channel.AutoGroup > model.AutoGroupTypeRegex {
			return invalidDump(path+".auto_group", "unsupported mode %d", channel.AutoGroup)
		}
		if channel.RPMLimit < 0 {
			return invalidDump(path+".rpm_limit", "must be non-negative")
		}
		if channel.MaxConcurrency < 0 {
			return invalidDump(path+".max_concurrency", "must be non-negative")
		}
		if len(channel.Model) > maxBackupModelListBytes || len(channel.CustomModel) > maxBackupModelListBytes {
			return invalidDump(path+".model", "model list is too large")
		}
		models, err := parseBackupCSV(path+".model", channel.Model, channel.CustomModel)
		if err != nil {
			return err
		}
		if channel.ParamOverride != nil && *channel.ParamOverride != "" {
			if err := validateJSONObject(path+".param_override", *channel.ParamOverride); err != nil {
				return err
			}
		}
		if channel.ChannelProxy != nil && strings.TrimSpace(*channel.ChannelProxy) != "" {
			if err := validateProxyURLForBackup(path+".channel_proxy", *channel.ChannelProxy); err != nil {
				return err
			}
		}
		if channel.MatchRegex != nil && *channel.MatchRegex != "" {
			if err := validateBackupRegex(path+".match_regex", *channel.MatchRegex); err != nil {
				return err
			}
		}
		if err := validateBackupHeaders(path+".custom_header", channel.CustomHeader); err != nil {
			return err
		}
		if len(channel.Keys) != 0 || channel.Stats != nil {
			return invalidDump(path, "embedded keys or stats are not allowed; use the top-level tables")
		}

		state.channelIDs[channel.ID] = channel
		state.channelModels[channel.ID] = models
	}

	keyValuesByChannel := make(map[int]map[string]struct{})
	keysByChannel := make(map[int][]model.ChannelKey)
	for i, key := range dump.ChannelKeys {
		path := fmt.Sprintf("channel_keys[%d]", i)
		if key.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := state.channelKeyIDs[key.ID]; exists {
			return invalidDump(path+".id", "duplicate channel key id %d", key.ID)
		}
		if _, exists := state.channelIDs[key.ChannelID]; !exists {
			return invalidDump(path+".channel_id", "references unknown channel %d", key.ChannelID)
		}
		if err := validateRequiredBackupString(path+".channel_key", key.ChannelKey, maxBackupSecretBytes); err != nil {
			return err
		}
		if key.StatusCode != 0 && (key.StatusCode < 100 || key.StatusCode > 599) {
			return invalidDump(path+".status_code", "must be 0 or between 100 and 599")
		}
		if key.LastUseTimeStamp < 0 || key.LastUseTimeStamp > model.MaxUnixTimestamp {
			return invalidDump(path+".last_use_time_stamp", "must be 0 or a valid Unix timestamp")
		}
		if key.RetryAfterUntil < 0 || key.RetryAfterUntil > model.MaxUnixTimestamp {
			return invalidDump(path+".retry_after_until", "must be 0 or a valid Unix timestamp")
		}
		if !finiteNonNegative(key.TotalCost) {
			return invalidDump(path+".total_cost", "must be a finite non-negative number")
		}
		if _, ok := keyValuesByChannel[key.ChannelID]; !ok {
			keyValuesByChannel[key.ChannelID] = make(map[string]struct{})
		}
		if _, exists := keyValuesByChannel[key.ChannelID][key.ChannelKey]; exists {
			return invalidDump(path+".channel_key", "duplicate key value for channel %d", key.ChannelID)
		}
		keyValuesByChannel[key.ChannelID][key.ChannelKey] = struct{}{}
		keysByChannel[key.ChannelID] = append(keysByChannel[key.ChannelID], key)
		state.channelKeyIDs[key.ID] = key
		state.channelKeyOwner[key.ID] = key.ChannelID
	}
	for _, channel := range dump.Channels {
		id := channel.ID
		validated := channel
		validated.BaseUrls = append([]model.BaseUrl(nil), channel.BaseUrls...)
		validated.CustomHeader = append([]model.CustomHeader(nil), channel.CustomHeader...)
		validated.HeaderRules = append([]model.HeaderRule(nil), channel.HeaderRules...)
		validated.JSONRewriteRules = append([]model.JSONRewriteRule(nil), channel.JSONRewriteRules...)
		for i := range validated.JSONRewriteRules {
			validated.JSONRewriteRules[i].Value = cloneBackupStringPointer(validated.JSONRewriteRules[i].Value)
		}
		validated.ChannelProxy = cloneBackupStringPointer(channel.ChannelProxy)
		validated.ParamOverride = cloneBackupStringPointer(channel.ParamOverride)
		validated.Keys = make([]model.ChannelKey, 0, len(keysByChannel[id]))
		validated.Keys = append(validated.Keys, keysByChannel[id]...)
		if err := model.ValidateChannel(&validated); err != nil {
			return invalidDump(fmt.Sprintf("channels[id=%d]", id), "%v", err)
		}
	}
	return nil
}

func validateDumpGroups(dump *model.DBDump, state *backupValidationState) error {
	canonicalNames := make(map[string]struct{}, len(dump.Groups))
	for i, group := range dump.Groups {
		path := fmt.Sprintf("groups[%d]", i)
		if group.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := state.groupIDs[group.ID]; exists {
			return invalidDump(path+".id", "duplicate group id %d", group.ID)
		}
		if err := validateRequiredBackupString(path+".name", group.Name, maxBackupNameBytes); err != nil {
			return err
		}
		if _, exists := state.groupNames[group.Name]; exists {
			return invalidDump(path+".name", "duplicate group name %q", group.Name)
		}
		canonicalName := strings.ToLower(group.Name)
		if _, exists := canonicalNames[canonicalName]; exists {
			return invalidDump(path+".name", "duplicates another group name case-insensitively")
		}
		canonicalNames[canonicalName] = struct{}{}
		if group.Mode < model.GroupModeRoundRobin || group.Mode > model.GroupModeWeighted {
			return invalidDump(path+".mode", "unsupported group mode %d", group.Mode)
		}
		if group.FirstTokenTimeOut < 0 || int64(group.FirstTokenTimeOut) > maxBackupDurationSeconds {
			return invalidDump(path+".first_token_time_out", "must be a safe non-negative duration")
		}
		if group.SessionKeepTime < 0 || int64(group.SessionKeepTime) > maxBackupDurationSeconds {
			return invalidDump(path+".session_keep_time", "must be a safe non-negative duration")
		}
		if group.MatchRegex != "" {
			if err := validateBackupRegex(path+".match_regex", group.MatchRegex); err != nil {
				return err
			}
		}
		if len(group.Items) != 0 {
			return invalidDump(path+".items", "embedded items are not allowed; use group_items")
		}
		validated := group
		if err := model.ValidateGroup(&validated); err != nil {
			return invalidDump(path, "%v", err)
		}
		state.groupIDs[group.ID] = group
		state.groupNames[group.Name] = struct{}{}
	}

	itemIDs := make(map[int]struct{}, len(dump.GroupItems))
	itemKeys := make(map[string]struct{}, len(dump.GroupItems))
	itemCounts := make(map[int]int, len(dump.Groups))
	graph := make(groupGraph, len(dump.Groups))
	for id := range state.groupIDs {
		graph[id] = nil
	}
	for i, item := range dump.GroupItems {
		path := fmt.Sprintf("group_items[%d]", i)
		if item.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := itemIDs[item.ID]; exists {
			return invalidDump(path+".id", "duplicate group item id %d", item.ID)
		}
		itemIDs[item.ID] = struct{}{}
		if _, exists := state.groupIDs[item.GroupID]; !exists {
			return invalidDump(path+".group_id", "references unknown group %d", item.GroupID)
		}
		itemCounts[item.GroupID]++
		if itemCounts[item.GroupID] > model.MaxGroupItems {
			return invalidDump(path+".group_id", "group %d exceeds the maximum of %d items", item.GroupID, model.MaxGroupItems)
		}
		if item.Priority < 0 || item.Priority > model.MaxGroupItemPriority {
			return invalidDump(path+".priority", "must be between 0 and %d", model.MaxGroupItemPriority)
		}
		if item.Weight < 1 || item.Weight > model.MaxGroupItemWeight {
			return invalidDump(path+".weight", "must be between 1 and %d", model.MaxGroupItemWeight)
		}
		switch item.Type {
		case model.GroupItemTypeChannel:
			if item.ChannelID <= 0 || item.TargetGroupID != 0 {
				return invalidDump(path, "channel item requires channel_id and forbids target_group_id")
			}
			if _, exists := state.channelIDs[item.ChannelID]; !exists {
				return invalidDump(path+".channel_id", "references unknown channel %d", item.ChannelID)
			}
			if err := validateRequiredBackupString(path+".model_name", item.ModelName, maxBackupNameBytes); err != nil {
				return err
			}
			if _, exists := state.channelModels[item.ChannelID][item.ModelName]; !exists {
				return invalidDump(path+".model_name", "model %q is not declared by channel %d", item.ModelName, item.ChannelID)
			}
		case model.GroupItemTypeGroup:
			if item.TargetGroupID <= 0 || item.ChannelID != 0 || item.ModelName != "" {
				return invalidDump(path, "group item requires target_group_id and forbids channel_id/model_name")
			}
			if _, exists := state.groupIDs[item.TargetGroupID]; !exists {
				return invalidDump(path+".target_group_id", "references unknown group %d", item.TargetGroupID)
			}
			graph[item.GroupID] = append(graph[item.GroupID], item.TargetGroupID)
		default:
			return invalidDump(path+".type", "unsupported group item type %q", item.Type)
		}
		uniqueKey := fmt.Sprintf("%d\x00%s\x00%d\x00%d\x00%s", item.GroupID, item.Type, item.ChannelID, item.TargetGroupID, item.ModelName)
		if _, exists := itemKeys[uniqueKey]; exists {
			return invalidDump(path, "duplicates another group item")
		}
		itemKeys[uniqueKey] = struct{}{}
	}
	depth, acyclic := backupGroupGraphDepth(graph)
	if !acyclic {
		return invalidDump("group_items", "group nesting contains a cycle")
	}
	if depth > MaxGroupNestDepth {
		return invalidDump("group_items", "group nesting depth %d exceeds maximum %d", depth, MaxGroupNestDepth)
	}
	return nil
}

// backupGroupGraphDepth uses a non-recursive topological pass so a maliciously
// deep dump cannot consume the goroutine stack or trigger quadratic validation.
func backupGroupGraphDepth(graph groupGraph) (int, bool) {
	indegree := make(map[int]int, len(graph))
	depth := make(map[int]int, len(graph))
	for node := range graph {
		indegree[node] = 0
	}
	for _, targets := range graph {
		for _, target := range targets {
			indegree[target]++
		}
	}
	queue := make([]int, 0, len(graph))
	for node, degree := range indegree {
		if degree == 0 {
			queue = append(queue, node)
		}
	}
	processed := 0
	maxDepth := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		processed++
		for _, target := range graph[node] {
			candidate := depth[node] + 1
			if candidate > depth[target] {
				depth[target] = candidate
				if candidate > maxDepth {
					maxDepth = candidate
				}
			}
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return maxDepth, processed == len(graph)
}

func validateDumpLLMInfos(infos []model.LLMInfo) error {
	seen := make(map[string]struct{}, len(infos))
	for i, info := range infos {
		path := fmt.Sprintf("llm_infos[%d]", i)
		validated := info
		if err := model.ValidateLLMInfo(&validated); err != nil {
			return invalidDump(path, "%v", err)
		}
		if validated.Name != info.Name {
			return invalidDump(path+".name", "must use normalized lowercase form %q", validated.Name)
		}
		if err := validateRequiredBackupString(path+".name", info.Name, maxBackupNameBytes); err != nil {
			return err
		}
		canonicalName := strings.ToLower(info.Name)
		if _, exists := seen[canonicalName]; exists {
			return invalidDump(path+".name", "duplicate model name %q", info.Name)
		}
		seen[canonicalName] = struct{}{}
		prices := []struct {
			name  string
			value float64
		}{{"input", info.Input}, {"output", info.Output}, {"cache_read", info.CacheRead}, {"cache_write", info.CacheWrite}}
		for _, price := range prices {
			if !finiteNonNegative(price.value) {
				return invalidDump(path+"."+price.name, "must be a finite non-negative number")
			}
		}
	}
	return nil
}

func validateDumpAPIKeys(keys []model.APIKey, state *backupValidationState) error {
	values := make(map[string]struct{}, len(keys))
	canonicalNames := make(map[string]struct{}, len(keys))
	for i, key := range keys {
		path := fmt.Sprintf("api_keys[%d]", i)
		validated := key
		if err := model.ValidateAPIKey(&validated); err != nil {
			return invalidDump(path, "%v", err)
		}
		if validated.Name != key.Name || validated.SupportedModels != key.SupportedModels {
			return invalidDump(path, "name and supported_models must already be normalized")
		}
		if key.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := state.apiKeyIDs[key.ID]; exists {
			return invalidDump(path+".id", "duplicate API key id %d", key.ID)
		}
		if err := validateRequiredBackupString(path+".name", key.Name, maxBackupNameBytes); err != nil {
			return err
		}
		if _, exists := state.apiKeyNames[key.Name]; exists {
			return invalidDump(path+".name", "duplicate API key name %q", key.Name)
		}
		canonicalName := strings.ToLower(key.Name)
		if _, exists := canonicalNames[canonicalName]; exists {
			return invalidDump(path+".name", "duplicates another API key name case-insensitively")
		}
		canonicalNames[canonicalName] = struct{}{}
		if err := validateRequiredBackupString(path+".api_key", key.APIKey, maxBackupSecretBytes); err != nil {
			return err
		}
		if _, exists := values[key.APIKey]; exists {
			return invalidDump(path+".api_key", "duplicate API key value")
		}
		if key.ExpireAt < 0 {
			return invalidDump(path+".expire_at", "must be non-negative")
		}
		if !finiteNonNegative(key.MaxCost) {
			return invalidDump(path+".max_cost", "must be a finite non-negative number")
		}
		if key.SupportedModels != "" {
			models, err := parseBackupCSV(path+".supported_models", key.SupportedModels)
			if err != nil {
				return err
			}
			for name := range models {
				if _, exists := state.groupNames[name]; !exists {
					return invalidDump(path+".supported_models", "references unknown group/model %q", name)
				}
			}
		}
		state.apiKeyIDs[key.ID] = key
		state.apiKeyNames[key.Name] = struct{}{}
		values[key.APIKey] = struct{}{}
	}
	return nil
}

func validateDumpSettings(settings []model.Setting) error {
	seen := make(map[model.SettingKey]struct{}, len(settings))
	for i := range settings {
		path := fmt.Sprintf("settings[%d]", i)
		if _, exists := seen[settings[i].Key]; exists {
			return invalidDump(path+".key", "duplicate setting key %q", settings[i].Key)
		}
		if err := settings[i].Validate(); err != nil {
			return invalidDump(path, "%v", err)
		}
		seen[settings[i].Key] = struct{}{}
	}
	return nil
}

func validateDumpStats(dump *model.DBDump, state *backupValidationState) error {
	if len(dump.StatsTotal) > 1 {
		return invalidDump("stats_total", "must contain at most one row")
	}
	for i, row := range dump.StatsTotal {
		path := fmt.Sprintf("stats_total[%d]", i)
		if row.ID != 1 {
			return invalidDump(path+".id", "must be 1")
		}
		if err := validateStatsMetrics(path, row.StatsMetrics); err != nil {
			return err
		}
	}
	dailyDates := make(map[string]struct{}, len(dump.StatsDaily))
	for i, row := range dump.StatsDaily {
		path := fmt.Sprintf("stats_daily[%d]", i)
		if err := validateBackupDate(path+".date", row.Date); err != nil {
			return err
		}
		if _, exists := dailyDates[row.Date]; exists {
			return invalidDump(path+".date", "duplicate date %q", row.Date)
		}
		dailyDates[row.Date] = struct{}{}
		if err := validateStatsMetrics(path, row.StatsMetrics); err != nil {
			return err
		}
	}
	hours := make(map[int]struct{}, len(dump.StatsHourly))
	for i, row := range dump.StatsHourly {
		path := fmt.Sprintf("stats_hourly[%d]", i)
		if row.Hour < 0 || row.Hour > 23 {
			return invalidDump(path+".hour", "must be between 0 and 23")
		}
		if _, exists := hours[row.Hour]; exists {
			return invalidDump(path+".hour", "duplicate hour %d", row.Hour)
		}
		hours[row.Hour] = struct{}{}
		if err := validateBackupDate(path+".date", row.Date); err != nil {
			return err
		}
		if err := validateStatsMetrics(path, row.StatsMetrics); err != nil {
			return err
		}
	}
	channelStats := make(map[int]struct{}, len(dump.StatsChannel))
	for i, row := range dump.StatsChannel {
		path := fmt.Sprintf("stats_channel[%d]", i)
		if _, exists := state.channelIDs[row.ChannelID]; !exists {
			return invalidDump(path+".channel_id", "references unknown channel %d", row.ChannelID)
		}
		if _, exists := channelStats[row.ChannelID]; exists {
			return invalidDump(path+".channel_id", "duplicate channel id %d", row.ChannelID)
		}
		channelStats[row.ChannelID] = struct{}{}
		if err := validateStatsMetrics(path, row.StatsMetrics); err != nil {
			return err
		}
	}
	apiKeyStats := make(map[int]struct{}, len(dump.StatsAPIKey))
	for i, row := range dump.StatsAPIKey {
		path := fmt.Sprintf("stats_api_key[%d]", i)
		if _, exists := state.apiKeyIDs[row.APIKeyID]; !exists {
			return invalidDump(path+".api_key_id", "references unknown API key %d", row.APIKeyID)
		}
		if _, exists := apiKeyStats[row.APIKeyID]; exists {
			return invalidDump(path+".api_key_id", "duplicate API key id %d", row.APIKeyID)
		}
		apiKeyStats[row.APIKeyID] = struct{}{}
		if err := validateStatsMetrics(path, row.StatsMetrics); err != nil {
			return err
		}
	}
	return nil
}

func validateDumpRelayLogs(logs []model.RelayLog, state *backupValidationState) error {
	seen := make(map[int64]struct{}, len(logs))
	validStatuses := map[model.AttemptStatus]struct{}{
		model.AttemptSuccess: {}, model.AttemptFailed: {}, model.AttemptClientCancel: {},
		model.AttemptCircuitBreak: {}, model.AttemptSkipped: {}, model.AttemptRedirect: {},
	}
	for i, relayLog := range logs {
		path := fmt.Sprintf("relay_logs[%d]", i)
		if relayLog.ID <= 0 {
			return invalidDump(path+".id", "must be positive")
		}
		if _, exists := seen[relayLog.ID]; exists {
			return invalidDump(path+".id", "duplicate relay log id %d", relayLog.ID)
		}
		seen[relayLog.ID] = struct{}{}
		if relayLog.Time < 0 || relayLog.Time > model.MaxUnixTimestamp || relayLog.InputTokens < 0 || relayLog.OutputTokens < 0 || relayLog.Ftut < 0 || relayLog.UseTime < 0 {
			return invalidDump(path, "time, token, and duration fields must be non-negative")
		}
		if !finiteNonNegative(relayLog.Cost) {
			return invalidDump(path+".cost", "must be a finite non-negative number")
		}
		if relayLog.ChannelId > 0 {
			if _, exists := state.channelIDs[relayLog.ChannelId]; !exists {
				return invalidDump(path+".channel", "references unknown channel %d", relayLog.ChannelId)
			}
		} else if relayLog.ChannelId < 0 {
			return invalidDump(path+".channel", "must be non-negative")
		}
		if relayLog.RequestAPIKeyName != "" {
			if _, exists := state.apiKeyNames[relayLog.RequestAPIKeyName]; !exists {
				return invalidDump(path+".request_api_key_name", "references unknown API key name %q", relayLog.RequestAPIKeyName)
			}
		}
		if relayLog.TotalAttempts < 0 || relayLog.TotalAttempts != len(relayLog.Attempts) {
			return invalidDump(path+".total_attempts", "must equal attempts length %d", len(relayLog.Attempts))
		}
		attemptNumbers := make(map[int]struct{}, len(relayLog.Attempts))
		for j, attempt := range relayLog.Attempts {
			attemptPath := fmt.Sprintf("%s.attempts[%d]", path, j)
			if attempt.AttemptNum <= 0 {
				return invalidDump(attemptPath+".attempt_num", "must be positive")
			}
			if _, exists := attemptNumbers[attempt.AttemptNum]; exists {
				return invalidDump(attemptPath+".attempt_num", "duplicate attempt number %d", attempt.AttemptNum)
			}
			attemptNumbers[attempt.AttemptNum] = struct{}{}
			if _, exists := validStatuses[attempt.Status]; !exists {
				return invalidDump(attemptPath+".status", "unsupported status %q", attempt.Status)
			}
			if attempt.Duration < 0 || attempt.FirstTokenTime < 0 {
				return invalidDump(attemptPath, "duration fields must be non-negative")
			}
			if attempt.ChannelID > 0 {
				if _, exists := state.channelIDs[attempt.ChannelID]; !exists {
					return invalidDump(attemptPath+".channel_id", "references unknown channel %d", attempt.ChannelID)
				}
			} else if attempt.ChannelID < 0 {
				return invalidDump(attemptPath+".channel_id", "must be non-negative")
			}
			if attempt.ChannelKeyID > 0 {
				owner, exists := state.channelKeyOwner[attempt.ChannelKeyID]
				if !exists || owner != attempt.ChannelID {
					return invalidDump(attemptPath+".channel_key_id", "does not belong to channel %d", attempt.ChannelID)
				}
			} else if attempt.ChannelKeyID < 0 {
				return invalidDump(attemptPath+".channel_key_id", "must be non-negative")
			}
		}
	}
	return nil
}

func validateStatsMetrics(path string, metrics model.StatsMetrics) error {
	if metrics.InputToken < 0 || metrics.OutputToken < 0 || metrics.WaitTime < 0 ||
		metrics.RequestSuccess < 0 || metrics.RequestFailed < 0 {
		return invalidDump(path, "statistics counters must be non-negative")
	}
	if !finiteNonNegative(metrics.InputCost) || !finiteNonNegative(metrics.OutputCost) {
		return invalidDump(path, "statistics costs must be finite non-negative numbers")
	}
	return nil
}

func validateRequiredBackupString(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return invalidDump(field, "must be valid UTF-8")
	}
	if value == "" || strings.TrimSpace(value) == "" {
		return invalidDump(field, "is required")
	}
	if value != strings.TrimSpace(value) {
		return invalidDump(field, "must not have leading or trailing whitespace")
	}
	if len(value) > maxBytes {
		return invalidDump(field, "exceeds maximum length of %d bytes", maxBytes)
	}
	return nil
}

func validateHTTPURL(field, value string) error {
	if err := validateRequiredBackupString(field, value, maxBackupURLBytes); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return invalidDump(field, "must be an absolute http or https URL")
	}
	if parsed.Fragment != "" {
		return invalidDump(field, "must not contain a fragment")
	}
	return nil
}

func validateProxyURLForBackup(field, value string) error {
	if err := validateRequiredBackupString(field, value, maxBackupURLBytes); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return invalidDump(field, "must be an absolute proxy URL")
	}
	switch parsed.Scheme {
	case "http", "https", "socks", "socks5":
		return nil
	default:
		return invalidDump(field, "unsupported proxy URL scheme %q", parsed.Scheme)
	}
}

func validateJSONObject(field, value string) error {
	if value != strings.TrimSpace(value) {
		return invalidDump(field, "must not have leading or trailing whitespace")
	}
	if len(value) > maxBackupJSONFieldBytes {
		return invalidDump(field, "exceeds maximum length of %d bytes", maxBackupJSONFieldBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return invalidDump(field, "must be a JSON object: %v", err)
	}
	if object == nil {
		return invalidDump(field, "must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return invalidDump(field, "must contain exactly one JSON object")
		}
		return invalidDump(field, "contains trailing data: %v", err)
	}
	return nil
}

func validateBackupRegex(field, pattern string) error {
	if len(pattern) > maxBackupRegexBytes {
		return invalidDump(field, "exceeds maximum length of %d bytes", maxBackupRegexBytes)
	}
	if _, err := regexp2.Compile(pattern, regexp2.ECMAScript); err != nil {
		return invalidDump(field, "is invalid: %v", err)
	}
	return nil
}

func validateBackupHeaders(field string, headers []model.CustomHeader) error {
	seen := make(map[string]struct{}, len(headers))
	for i, header := range headers {
		path := fmt.Sprintf("%s[%d]", field, i)
		if len(header.HeaderKey) > maxBackupHeaderBytes || !validHTTPHeaderName(header.HeaderKey) {
			return invalidDump(path+".header_key", "is not a valid HTTP header name")
		}
		if len(header.HeaderValue) > maxBackupHeaderBytes || !utf8.ValidString(header.HeaderValue) ||
			strings.ContainsAny(header.HeaderValue, "\r\n") {
			return invalidDump(path+".header_value", "is not a valid HTTP header value")
		}
		key := strings.ToLower(header.HeaderKey)
		if _, exists := seen[key]; exists {
			return invalidDump(path+".header_key", "duplicates header %q", header.HeaderKey)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func parseBackupCSV(field string, values ...string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for _, value := range values {
		if len(value) > maxBackupModelListBytes {
			return nil, invalidDump(field, "exceeds maximum length of %d bytes", maxBackupModelListBytes)
		}
		if value == "" {
			continue
		}
		for _, item := range strings.Split(value, ",") {
			name := strings.TrimSpace(item)
			if err := validateRequiredBackupString(field, name, maxBackupNameBytes); err != nil {
				return nil, err
			}
			if _, exists := result[name]; exists {
				return nil, invalidDump(field, "contains duplicate model %q", name)
			}
			result[name] = struct{}{}
		}
	}
	return result, nil
}

func validateBackupDate(field, value string) error {
	parsed, err := time.Parse("20060102", value)
	if err != nil || parsed.Format("20060102") != value {
		return invalidDump(field, "must use YYYYMMDD format")
	}
	return nil
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func cloneBackupStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
