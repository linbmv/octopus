package op

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

// ChannelBaselineCreate stores a verified request shape and prunes older
// records for the exact channel/key/model/endpoint/scope tuple. The operation
// is intentionally independent from the relay response path: callers may
// ignore a persistence error without failing the user request.
func ChannelBaselineCreate(ctx context.Context, baseline *model.ChannelBaseline) error {
	if baseline == nil {
		return errors.New("channel baseline is nil")
	}
	baseline.Normalize()
	if !baseline.Valid() {
		return errors.New("invalid channel baseline")
	}
	if baseline.EndpointFingerprint != model.CapabilityEndpointFingerprint(baseline.Endpoint) {
		return errors.New("channel baseline endpoint fingerprint mismatch")
	}
	database := db.GetDB()
	if database == nil {
		return errors.New("database is not initialized")
	}

	tx := database.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if recoverValue := recover(); recoverValue != nil {
			tx.Rollback()
			panic(recoverValue)
		}
	}()
	if err := tx.Create(baseline).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("create channel baseline: %w", err)
	}

	var staleIDs []uint
	query := tx.Model(&model.ChannelBaseline{}).
		Where("channel_id = ? AND channel_key_id = ? AND model = ? AND wire_protocol = ? AND endpoint_fingerprint = ? AND scope_fingerprint = ?",
			baseline.ChannelID, baseline.ChannelKeyID, baseline.Model, baseline.WireProtocol,
			baseline.EndpointFingerprint, baseline.ScopeFingerprint).
		Order("captured_at DESC, id DESC").
		Offset(model.ChannelBaselineKeepPerScope).
		Pluck("id", &staleIDs)
	if query.Error != nil {
		tx.Rollback()
		return fmt.Errorf("find stale channel baselines: %w", query.Error)
	}
	if len(staleIDs) > 0 {
		if err := tx.Where("id IN ?", staleIDs).Delete(&model.ChannelBaseline{}).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("prune channel baselines: %w", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("commit channel baseline: %w", err)
	}
	return nil
}

func ChannelBaselineLatest(
	ctx context.Context,
	channelID, channelKeyID int,
	modelName string,
	protocol string,
	endpointFingerprint string,
	scopeFingerprint string,
) (*model.ChannelBaseline, error) {
	if channelID <= 0 || channelKeyID <= 0 || modelName == "" || protocol == "" || endpointFingerprint == "" || scopeFingerprint == "" {
		return nil, errors.New("invalid channel baseline scope")
	}
	database := db.GetDB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	var baseline model.ChannelBaseline
	err := database.WithContext(ctx).
		Where("channel_id = ? AND channel_key_id = ? AND model = ? AND wire_protocol = ? AND endpoint_fingerprint = ? AND scope_fingerprint = ? AND expires_at > ?",
			channelID, channelKeyID, modelName, protocol, endpointFingerprint, scopeFingerprint, time.Now().UTC()).
		Order("captured_at DESC, id DESC").
		First(&baseline).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &baseline, nil
}

func ChannelBaselineList(ctx context.Context, channelID int, limit int) ([]model.ChannelBaseline, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid channel id")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	database := db.GetDB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	var baselines []model.ChannelBaseline
	if err := database.WithContext(ctx).Where("channel_id = ?", channelID).
		Order("captured_at DESC, id DESC").Limit(limit).Find(&baselines).Error; err != nil {
		return nil, err
	}
	return baselines, nil
}

func ChannelBaselineCleanup(ctx context.Context, now time.Time) error {
	database := db.GetDB()
	if database == nil {
		return errors.New("database is not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return database.WithContext(ctx).Where("expires_at <= ?", now).Delete(&model.ChannelBaseline{}).Error
}

func deleteChannelBaselinesChannelTx(tx *gorm.DB, channelID int) error {
	if tx == nil || channelID <= 0 {
		return nil
	}
	return tx.Where("channel_id = ?", channelID).Delete(&model.ChannelBaseline{}).Error
}

func deleteChannelBaselinesKeysTx(tx *gorm.DB, channelID int, keyIDs []int) error {
	if tx == nil || channelID <= 0 || len(keyIDs) == 0 {
		return nil
	}
	return tx.Where("channel_id = ? AND channel_key_id IN ?", channelID, keyIDs).Delete(&model.ChannelBaseline{}).Error
}
