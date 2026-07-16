package op

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CapabilityEvidenceUpsert(ctx context.Context, evidence *model.CapabilityEvidence) error {
	if evidence == nil || evidence.ChannelID <= 0 || evidence.ChannelKeyID <= 0 {
		return fmt.Errorf("invalid capability evidence scope")
	}
	evidence.Model = strings.TrimSpace(evidence.Model)
	if evidence.Model == "" || len(evidence.Model) > model.MaxModelNameBytes {
		return fmt.Errorf("invalid capability evidence model")
	}
	if evidence.WireProtocol == "" || len(evidence.WireProtocol) > 64 {
		return fmt.Errorf("invalid capability evidence wire protocol")
	}
	if !evidence.Capability.Valid() || !evidence.Status.Valid() {
		return fmt.Errorf("invalid capability evidence result")
	}
	if evidence.ScopeFingerprint == "" {
		return fmt.Errorf("capability evidence scope fingerprint is empty")
	}
	evidence.Endpoint = strings.TrimSpace(evidence.Endpoint)
	if evidence.Endpoint == "" || len(evidence.Endpoint) > 2048 || evidence.EndpointFingerprint == "" {
		return fmt.Errorf("invalid capability evidence endpoint")
	}
	if evidence.ProbedAt.IsZero() || !evidence.ExpiresAt.After(evidence.ProbedAt) {
		return fmt.Errorf("invalid capability evidence lifetime")
	}
	evidence.ErrorClass = truncateCapabilityText(evidence.ErrorClass, 96)
	evidence.ErrorMessage = truncateCapabilityText(evidence.ErrorMessage, 512)
	if evidence.Source == "" {
		evidence.Source = "probe"
	}

	updates := map[string]any{
		"endpoint":          evidence.Endpoint,
		"status":            evidence.Status,
		"error_class":       evidence.ErrorClass,
		"error_message":     evidence.ErrorMessage,
		"http_status":       evidence.HTTPStatus,
		"scope_fingerprint": evidence.ScopeFingerprint,
		"source":            evidence.Source,
		"probed_at":         evidence.ProbedAt,
		"expires_at":        evidence.ExpiresAt,
		"updated_at":        time.Now().UTC(),
	}
	return db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "channel_id"}, {Name: "channel_key_id"}, {Name: "model"},
			{Name: "wire_protocol"}, {Name: "capability"}, {Name: "endpoint_fingerprint"},
		},
		DoUpdates: clause.Assignments(updates),
	}).Create(evidence).Error
}

func CapabilityEvidenceList(ctx context.Context, channelID int) ([]model.CapabilityEvidence, error) {
	if channelID <= 0 {
		return nil, fmt.Errorf("invalid channel id")
	}
	var evidence []model.CapabilityEvidence
	if err := db.GetDB().WithContext(ctx).
		Where("channel_id = ?", channelID).
		Order("channel_key_id ASC, model ASC, wire_protocol ASC, capability ASC").
		Find(&evidence).Error; err != nil {
		return nil, err
	}
	return evidence, nil
}

func deleteCapabilityEvidenceChannelTx(tx *gorm.DB, channelID int) error {
	if channelID <= 0 {
		return nil
	}
	return tx.Where("channel_id = ?", channelID).Delete(&model.CapabilityEvidence{}).Error
}

func deleteCapabilityEvidenceKeysTx(tx *gorm.DB, channelID int, keyIDs []int) error {
	if channelID <= 0 || len(keyIDs) == 0 {
		return nil
	}
	return tx.Where("channel_id = ? AND channel_key_id IN ?", channelID, keyIDs).
		Delete(&model.CapabilityEvidence{}).Error
}

// RankChannelKeysByCapability keeps every available key as a fallback. Fresh
// positive evidence is preferred, missing/stale/transient evidence remains in
// the middle, and fresh hard-negative evidence is attempted last.
func RankChannelKeysByCapability(
	ctx context.Context,
	channel *model.Channel,
	keys []model.ChannelKey,
	modelName string,
	required []model.Capability,
	endpoint string,
) []model.ChannelKey {
	if channel == nil || len(keys) <= 1 || strings.TrimSpace(modelName) == "" || len(required) == 0 || strings.TrimSpace(endpoint) == "" {
		return keys
	}
	ranks, ok := capabilityKeyRanks(ctx, channel, keys, modelName, required, endpoint)
	if !ok {
		return keys
	}
	result := append([]model.ChannelKey(nil), keys...)
	sort.SliceStable(result, func(i, j int) bool { return ranks[result[i].ID] < ranks[result[j].ID] })
	return result
}

// CapabilityChannelRank returns the best usable account tier for one channel:
// 0 supported, 1 unknown/stale/transient, 2 definitive negative. A channel is
// negative only when every currently available account is negative.
func CapabilityChannelRank(
	ctx context.Context,
	channel *model.Channel,
	modelName string,
	required []model.Capability,
	endpoint string,
) int {
	if channel == nil || strings.TrimSpace(modelName) == "" || len(required) == 0 || strings.TrimSpace(endpoint) == "" {
		return 1
	}
	keys := channel.AvailableKeys()
	if len(keys) == 0 {
		return 1
	}
	ranks, ok := capabilityKeyRanks(ctx, channel, keys, modelName, required, endpoint)
	if !ok {
		return 1
	}
	best := 2
	for _, key := range keys {
		if ranks[key.ID] < best {
			best = ranks[key.ID]
		}
	}
	return best
}

func capabilityKeyRanks(
	ctx context.Context,
	channel *model.Channel,
	keys []model.ChannelKey,
	modelName string,
	required []model.Capability,
	endpoint string,
) (map[int]int, bool) {
	keyIDs := make([]int, 0, len(keys))
	for _, key := range keys {
		if key.ID > 0 {
			keyIDs = append(keyIDs, key.ID)
		}
	}
	if len(keyIDs) == 0 {
		return nil, false
	}

	now := time.Now().UTC()
	var evidence []model.CapabilityEvidence
	database := db.GetDB()
	if database == nil {
		return nil, false
	}
	err := database.WithContext(ctx).
		Where("channel_id = ? AND channel_key_id IN ? AND model = ? AND wire_protocol = ? AND capability IN ? AND endpoint_fingerprint = ? AND expires_at > ?",
			channel.ID, keyIDs, strings.TrimSpace(modelName), channel.Type, required, model.CapabilityEndpointFingerprint(endpoint), now).
		Find(&evidence).Error
	if err != nil {
		log.WithContext(ctx).Warnw("capability evidence lookup failed; preserving key order",
			"channel_id", channel.ID, "error", err)
		return nil, false
	}

	type keyCapability struct {
		keyID      int
		capability model.Capability
	}
	byKey := make(map[keyCapability]model.CapabilityEvidence, len(evidence))
	for _, item := range evidence {
		byKey[keyCapability{keyID: item.ChannelKeyID, capability: item.Capability}] = item
	}
	rank := make(map[int]int, len(keys))
	for _, key := range keys {
		fingerprint := model.CapabilityScopeFingerprint(channel, key, endpoint)
		allSupported := true
		hardNegative := false
		for _, capability := range required {
			item, ok := byKey[keyCapability{keyID: key.ID, capability: capability}]
			if !ok || item.ScopeFingerprint != fingerprint {
				allSupported = false
				continue
			}
			switch item.Status {
			case model.CapabilitySupported:
			case model.CapabilityUnsupported, model.CapabilityUnauthorized, model.CapabilityNotImplemented:
				allSupported = false
				hardNegative = true
			default:
				allSupported = false
			}
		}
		switch {
		case hardNegative:
			rank[key.ID] = 2
		case allSupported:
			rank[key.ID] = 0
		default:
			rank[key.ID] = 1
		}
	}

	return rank, true
}

func truncateCapabilityText(value string, max int) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value))
	for _, pattern := range capabilitySecretPatterns {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

var capabilitySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b(api[_ -]?key|token|credential)\s*[:=]\s*[^\s,;]+`),
}
