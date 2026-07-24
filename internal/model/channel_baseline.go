package model

import (
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
)

const (
	ChannelBaselineSourceRelaySuccess = "relay_success"
	ChannelBaselineSourceManualLive   = "manual_live"
	ChannelBaselineSourceGoldenImport = "golden_import"
	ChannelBaselineKeepPerScope       = 3
)

// ChannelBaseline is a short-lived, redacted record of a request shape that
// received a verified upstream success. It intentionally is not part of the
// database backup format because it is runtime evidence, not configuration.
type ChannelBaseline struct {
	ID                  uint                     `json:"id" gorm:"primaryKey"`
	ChannelID           int                      `json:"channel_id" gorm:"not null;index:idx_channel_baseline_scope,priority:1"`
	ChannelKeyID        int                      `json:"channel_key_id" gorm:"not null;index:idx_channel_baseline_scope,priority:2"`
	Model               string                   `json:"model" gorm:"size:256;not null;index:idx_channel_baseline_scope,priority:3"`
	WireProtocol        llm.APIFormat            `json:"wire_protocol" gorm:"size:64;not null;index:idx_channel_baseline_scope,priority:4"`
	Endpoint            string                   `json:"endpoint,omitempty" gorm:"size:2048;not null"`
	EndpointFingerprint string                   `json:"-" gorm:"size:64;not null;index:idx_channel_baseline_scope,priority:5"`
	ScopeFingerprint    string                   `json:"-" gorm:"size:64;not null;index:idx_channel_baseline_scope,priority:6"`
	RequestShape        requestartifact.Artifact `json:"request_shape" gorm:"serializer:json;not null"`
	HTTPStatus          int                      `json:"http_status,omitempty"`
	ContentType         string                   `json:"content_type,omitempty" gorm:"size:256"`
	Source              string                   `json:"source" gorm:"size:32;not null;index"`
	CapturedAt          time.Time                `json:"captured_at" gorm:"not null;index"`
	ExpiresAt           time.Time                `json:"expires_at" gorm:"not null;index"`
	Version             int                      `json:"version" gorm:"not null;default:1"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
}

func (b *ChannelBaseline) Normalize() {
	if b == nil {
		return
	}
	b.Model = strings.TrimSpace(b.Model)
	b.Endpoint = strings.TrimSpace(b.Endpoint)
	b.EndpointFingerprint = strings.TrimSpace(b.EndpointFingerprint)
	b.ScopeFingerprint = strings.TrimSpace(b.ScopeFingerprint)
	b.ContentType = strings.TrimSpace(b.ContentType)
	b.Source = strings.TrimSpace(b.Source)
	if b.Version <= 0 {
		b.Version = 1
	}
}

func (b *ChannelBaseline) Valid() bool {
	if b == nil {
		return false
	}
	b.Normalize()
	if b.ChannelID <= 0 || b.ChannelKeyID <= 0 || b.Model == "" || len(b.Model) > MaxModelNameBytes ||
		b.WireProtocol == "" || b.Endpoint == "" || len(b.Endpoint) > 2048 ||
		b.EndpointFingerprint == "" || b.ScopeFingerprint == "" || b.RequestShape.ShapeSHA256 == "" ||
		b.CapturedAt.IsZero() || !b.ExpiresAt.After(b.CapturedAt) {
		return false
	}
	switch b.Source {
	case ChannelBaselineSourceRelaySuccess, ChannelBaselineSourceManualLive, ChannelBaselineSourceGoldenImport:
		return true
	default:
		return false
	}
}
