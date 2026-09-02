package op

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

// edgeV2Dump is deliberately a private wire model. Importing Edge's model
// package would couple the C06/upstream base to the feature branch and would
// also make excluded OAuth/self-healing fields easy to reintroduce by accident.
type edgeV2Dump struct {
	Version      int                `json:"version"`
	ExportedAt   time.Time          `json:"exported_at"`
	IncludeLogs  bool               `json:"include_logs"`
	IncludeStats bool               `json:"include_stats"`
	Channels     []edgeV2Channel    `json:"channels,omitempty"`
	ChannelKeys  []edgeV2ChannelKey `json:"channel_keys,omitempty"`
	Groups       []edgeV2Group      `json:"groups,omitempty"`
	GroupItems   []edgeV2GroupItem  `json:"group_items,omitempty"`
	APIKeys      []model.APIKey     `json:"api_keys,omitempty"`
	Settings     []model.Setting    `json:"settings,omitempty"`
}

type edgeV2Channel struct {
	ID           int                  `json:"id"`
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	Enabled      bool                 `json:"enabled"`
	BaseUrls     []model.BaseUrl      `json:"base_urls"`
	Keys         []edgeV2ChannelKey   `json:"keys,omitempty"`
	Model        string               `json:"model"`
	CustomModel  string               `json:"custom_model"`
	Proxy        bool                 `json:"proxy"`
	AutoSync     bool                 `json:"auto_sync"`
	CustomHeader []model.CustomHeader `json:"custom_header"`
	// Edge 的高级改写规则；旧版本导出文件没有这两个字段，缺省为空即保持原有行为。
	HeaderRules      []model.HeaderRule      `json:"header_rules,omitempty"`
	JSONRewriteRules []model.JSONRewriteRule `json:"json_rewrite_rules,omitempty"`
	ParamOverride    *string                 `json:"param_override"`
	ChannelProxy     *string                 `json:"channel_proxy"`
	MatchRegex       *string                 `json:"match_regex"`
}

type edgeV2ChannelKey struct {
	ID               int    `json:"id"`
	ChannelID        int    `json:"channel_id"`
	Enabled          bool   `json:"enabled"`
	ChannelKey       string `json:"channel_key"`
	StatusCode       int    `json:"status_code"`
	LastUseTimeStamp int64  `json:"last_use_time_stamp"`
	RetryAfterUntil  int64  `json:"retry_after_until"`
	Remark           string `json:"remark"`
}

type edgeV2Group struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Mode              int    `json:"mode"`
	MatchRegex        string `json:"match_regex"`
	FirstTokenTimeOut int    `json:"first_token_time_out"`
	SessionKeepTime   int    `json:"session_keep_time"`
}

type edgeV2GroupItem struct {
	ID            int    `json:"id"`
	GroupID       int    `json:"group_id"`
	Type          string `json:"type"`
	ChannelID     int    `json:"channel_id"`
	TargetGroupID int    `json:"target_group_id"`
	ModelName     string `json:"model_name"`
	Priority      int    `json:"priority"`
	Weight        int    `json:"weight"`
	Disabled      bool   `json:"disabled"`
}

// ConvertEdgeV2Config turns an Edge v2 dump into the C06 config
// wire format. Numeric IDs are retained for the empty-target migration path;
// importing into a populated target remains subject to the normal incremental
// ID conflict semantics and is reported by the import result.
//
// The input is the decrypted JSON payload. Callers that accept an encrypted
// backup must authenticate and decrypt it before calling this function.
func ConvertEdgeV2Config(data []byte) (*model.ConfigDump, error) {
	var edge edgeV2Dump
	if err := json.Unmarshal(data, &edge); err != nil {
		return nil, ErrDBBackupInvalidEnvelope
	}
	if edge.Version != 2 {
		return nil, ErrDBBackupUnsupported
	}
	dump := &model.ConfigDump{
		Version:    configDumpVersion,
		Scope:      model.ConfigDumpScope,
		ExportedAt: edge.ExportedAt,
		APIKeys:    append([]model.APIKey(nil), edge.APIKeys...),
	}
	if dump.ExportedAt.IsZero() {
		dump.ExportedAt = time.Now().UTC()
	}
	warnings := warningCollector{}
	if edge.IncludeLogs {
		warnings.add("Edge v2 logs were excluded from configuration import")
	}
	if edge.IncludeStats {
		warnings.add("Edge v2 statistics were excluded from configuration import")
	}

	keysByChannel := make(map[int][]edgeV2ChannelKey, len(edge.Channels))
	for _, key := range edge.ChannelKeys {
		keysByChannel[key.ChannelID] = append(keysByChannel[key.ChannelID], key)
	}
	includedChannels := make(map[int]bool, len(edge.Channels))
	channelsByID := make(map[int]edgeV2Channel, len(edge.Channels))
	for _, source := range edge.Channels {
		channelsByID[source.ID] = source
		provider, ok := mapEdgeProvider(source.Type)
		if source.Type == "openai/codex" {
			warnings.addf("channel %d (%s) uses Codex OAuth and was skipped", source.ID, source.Name)
			continue
		}
		if !ok {
			warnings.addf("channel %d (%s) uses unsupported provider %q and was skipped", source.ID, source.Name, source.Type)
			continue
		}
		baseURLs := append([]model.BaseUrl(nil), source.BaseUrls...)
		primaryURL := ""
		if len(baseURLs) > 0 {
			primaryURL = baseURLs[0].URL
		}
		channelKeys := append([]edgeV2ChannelKey(nil), keysByChannel[source.ID]...)
		channelKeys = append(channelKeys, source.Keys...)
		configKeys := make([]model.ConfigChannelKey, 0, len(channelKeys))
		primaryKey := ""
		for _, key := range channelKeys {
			if strings.TrimSpace(key.ChannelKey) == "" {
				continue
			}
			if primaryKey == "" {
				primaryKey = key.ChannelKey
			}
			configKeys = append(configKeys, model.ConfigChannelKey{
				ID: key.ID, ChannelID: source.ID, Enabled: key.Enabled,
				ChannelKey: key.ChannelKey, StatusCode: key.StatusCode,
				LastUseTimeStamp: key.LastUseTimeStamp, RetryAfterUntil: key.RetryAfterUntil,
				Remark: key.Remark,
			})
		}
		dump.Channels = append(dump.Channels, model.ConfigChannel{
			ID: source.ID, Name: source.Name, Type: provider, Enabled: source.Enabled,
			BaseURL: primaryURL, BaseUrls: baseURLs, Key: primaryKey, Keys: configKeys,
			Proxy: source.Proxy, AutoSync: source.AutoSync, CustomHeader: source.CustomHeader,
			HeaderRules: source.HeaderRules, JSONRewriteRules: source.JSONRewriteRules,
			ParamOverride: source.ParamOverride, ChannelProxy: source.ChannelProxy,
			MatchRegex: source.MatchRegex,
		})
		includedChannels[source.ID] = true
	}

	modelIDs := make(map[channelModelKey]int)
	nextModelID := 1
	addModel := func(channelID int, name string, source model.ChannelModelSource) int {
		name = strings.TrimSpace(name)
		if name == "" || !includedChannels[channelID] {
			return 0
		}
		key := channelModelKey{channelID: channelID, name: name}
		if id, ok := modelIDs[key]; ok {
			return id
		}
		id := nextModelID
		nextModelID++
		modelIDs[key] = id
		dump.ChannelModels = append(dump.ChannelModels, model.ConfigChannelModel{ID: id, ChannelID: channelID, Name: name, Source: source})
		return id
	}
	channelIDs := make([]int, 0, len(channelsByID))
	for id := range channelsByID {
		if includedChannels[id] {
			channelIDs = append(channelIDs, id)
		}
	}
	sort.Ints(channelIDs)
	for _, id := range channelIDs {
		source := channelsByID[id]
		for _, name := range splitEdgeModels(source.Model) {
			modelSource := model.ChannelModelSourceManual
			if source.AutoSync {
				modelSource = model.ChannelModelSourceAuto
			}
			addModel(id, name, modelSource)
		}
		for _, name := range splitEdgeModels(source.CustomModel) {
			addModel(id, name, model.ChannelModelSourceManual)
		}
	}

	groupIDs := make(map[int]bool, len(edge.Groups))
	for _, source := range edge.Groups {
		mode := model.GroupModeFailover
		switch source.Mode {
		case 1:
			mode = model.GroupModeRoundRobin
		case 2:
			mode = model.GroupModeRandom
		case 3:
			mode = model.GroupModeFailover
		case 4:
			mode = model.GroupModeWeighted
		case 0:
			mode = model.GroupModeManual
			warnings.addf("group %d (%s) had no Edge mode; imported as manual", source.ID, source.Name)
		default:
			warnings.addf("group %d (%s) Edge mode %d mapped to failover", source.ID, source.Name, source.Mode)
		}
		if source.MatchRegex != "" {
			warnings.addf("group %d (%s) match_regex was not representable and was omitted", source.ID, source.Name)
		}
		if source.FirstTokenTimeOut != 0 || source.SessionKeepTime != 0 {
			warnings.addf("group %d (%s) timeout/session settings were not representable and were omitted", source.ID, source.Name)
		}
		dump.Groups = append(dump.Groups, model.ConfigGroup{ID: source.ID, Name: source.Name, Enabled: source.Enabled, Mode: mode, RelayConfig: model.DefaultGroupRelayConfig()})
		groupIDs[source.ID] = true
	}
	for _, source := range edge.GroupItems {
		if !groupIDs[source.GroupID] {
			warnings.addf("group item %d references missing group %d and was skipped", source.ID, source.GroupID)
			continue
		}
		item := model.ConfigGroupItem{ID: source.ID, GroupID: source.GroupID, Priority: source.Priority, Disabled: source.Disabled}
		item.Weight = source.Weight
		if item.Weight <= 0 {
			item.Weight = 1
		}
		switch strings.ToLower(strings.TrimSpace(source.Type)) {
		case "group":
			if !groupIDs[source.TargetGroupID] {
				warnings.addf("group item %d references missing target group %d and was skipped", source.ID, source.TargetGroupID)
				continue
			}
			item.Type = model.GroupItemTypeGroup
			item.TargetGroupID = intPtr(source.TargetGroupID)
		default:
			item.Type = model.GroupItemTypeChannelModel
			modelID := addModel(source.ChannelID, source.ModelName, model.ChannelModelSourceManual)
			if modelID == 0 {
				warnings.addf("group item %d references skipped/missing channel %d and was skipped", source.ID, source.ChannelID)
				continue
			}
			item.ChannelModelID = intPtr(modelID)
		}
		dump.GroupItems = append(dump.GroupItems, item)
	}
	for _, setting := range edge.Settings {
		if edgeSettingSupported(setting.Key) {
			dump.Settings = append(dump.Settings, setting)
		} else {
			warnings.addf("setting %q was not supported by C06 and was omitted", setting.Key)
		}
	}
	dump.Warnings = warnings.list()
	return dump, nil
}

type channelModelKey struct {
	channelID int
	name      string
}

func splitEdgeModels(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}

func mapEdgeProvider(value string) (model.ChannelProvider, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai", "openai/chat_completions":
		return model.ChannelProviderOpenAI, true
	case "openai/responses":
		return model.ChannelProviderOpenAIResponses, true
	case "anthropic", "anthropic/messages":
		return model.ChannelProviderAnthropic, true
	case "gemini", "gemini/contents":
		return model.ChannelProviderGemini, true
	case "doubao", "volcengine":
		return model.ChannelProviderVolcengine, true
	default:
		return "", false
	}
}

func edgeSettingSupported(key model.SettingKey) bool {
	switch key {
	case model.SettingKeyProxyURL, model.SettingKeyStatsSaveInterval,
		model.SettingKeyModelInfoUpdateInterval, model.SettingKeySyncLLMInterval,
		model.SettingKeyCORSAllowOrigins:
		return true
	default:
		return false
	}
}

func intPtr(value int) *int { return &value }

type warningCollector struct {
	items []string
	seen  map[string]struct{}
}

func (w *warningCollector) add(value string) {
	if w.seen == nil {
		w.seen = make(map[string]struct{})
	}
	if _, ok := w.seen[value]; ok {
		return
	}
	w.seen[value] = struct{}{}
	w.items = append(w.items, value)
}

func (w *warningCollector) addf(format string, args ...any) { w.add(fmt.Sprintf(format, args...)) }
func (w *warningCollector) list() []string                  { return append([]string(nil), w.items...) }
