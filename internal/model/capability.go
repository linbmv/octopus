package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/looplj/axonhub/llm"
)

type Capability string

const (
	CapabilityText   Capability = "text"
	CapabilityStream Capability = "stream"
	CapabilityTool   Capability = "tool"
	CapabilityVision Capability = "vision"
)

func (c Capability) Valid() bool {
	switch c {
	case CapabilityText, CapabilityStream, CapabilityTool, CapabilityVision:
		return true
	default:
		return false
	}
}

type CapabilityStatus string

const (
	CapabilitySupported      CapabilityStatus = "supported"
	CapabilityUnsupported    CapabilityStatus = "unsupported"
	CapabilityUnauthorized   CapabilityStatus = "unauthorized"
	CapabilityNotImplemented CapabilityStatus = "not_implemented"
	CapabilityTransient      CapabilityStatus = "transient"
)

func (s CapabilityStatus) Valid() bool {
	switch s {
	case CapabilitySupported, CapabilityUnsupported, CapabilityUnauthorized,
		CapabilityNotImplemented, CapabilityTransient:
		return true
	default:
		return false
	}
}

// CapabilityEvidence is short-lived runtime evidence and is deliberately not
// part of database backups. ScopeFingerprint invalidates evidence when an
// account secret, endpoint, protocol, or outbound rewrite configuration changes.
type CapabilityEvidence struct {
	ID                  uint             `json:"id" gorm:"primaryKey"`
	ChannelID           int              `json:"channel_id" gorm:"not null;uniqueIndex:idx_capability_scope,priority:1;index"`
	ChannelKeyID        int              `json:"channel_key_id" gorm:"not null;uniqueIndex:idx_capability_scope,priority:2;index"`
	Model               string           `json:"model" gorm:"size:256;not null;uniqueIndex:idx_capability_scope,priority:3"`
	WireProtocol        llm.APIFormat    `json:"wire_protocol" gorm:"size:64;not null;uniqueIndex:idx_capability_scope,priority:4"`
	Capability          Capability       `json:"capability" gorm:"size:32;not null;uniqueIndex:idx_capability_scope,priority:5"`
	Endpoint            string           `json:"endpoint,omitempty" gorm:"size:2048;not null"`
	EndpointFingerprint string           `json:"-" gorm:"size:64;not null;uniqueIndex:idx_capability_scope,priority:6"`
	Status              CapabilityStatus `json:"status" gorm:"size:32;not null;index"`
	ErrorClass          string           `json:"error_class,omitempty" gorm:"size:96"`
	ErrorLevel          string           `json:"error_level,omitempty" gorm:"size:16"`
	ErrorMessage        string           `json:"error_message,omitempty" gorm:"size:512"`
	HTTPStatus          int              `json:"http_status,omitempty"`
	ScopeFingerprint    string           `json:"-" gorm:"size:64;not null"`
	Source              string           `json:"source" gorm:"size:16;not null;default:probe"`
	ProbedAt            time.Time        `json:"probed_at" gorm:"not null;index"`
	ExpiresAt           time.Time        `json:"expires_at" gorm:"not null;index"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

// CapabilityScopeFingerprint binds evidence to all channel settings that can
// alter the authenticated outbound request without storing another copy of the
// credential itself.
func CapabilityScopeFingerprint(channel *Channel, key ChannelKey, endpoint string) string {
	if channel == nil {
		return ""
	}
	payload := struct {
		Type             llm.APIFormat
		Endpoint         string
		Key              string
		Proxy            bool
		ChannelProxy     *string
		CustomHeader     []CustomHeader
		HeaderRules      []HeaderRule
		JSONRewriteRules []JSONRewriteRule
		ParamOverride    *string
		RawPassthrough   bool
		UserAgent        string
	}{
		Type:             channel.Type,
		Endpoint:         strings.TrimSpace(endpoint),
		Key:              key.ChannelKey,
		Proxy:            channel.Proxy,
		ChannelProxy:     channel.ChannelProxy,
		CustomHeader:     channel.CustomHeader,
		HeaderRules:      channel.HeaderRules,
		JSONRewriteRules: channel.JSONRewriteRules,
		ParamOverride:    channel.ParamOverride,
		RawPassthrough:   channel.RawPassthrough,
		UserAgent:        strings.TrimSpace(channel.UserAgent),
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func CapabilityEndpointFingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return hex.EncodeToString(sum[:])
}

// RequiredCapabilities returns only independently probed request features.
// Plain requests use text; feature-bearing requests require each feature they
// actually exercise, so combined streaming tool/vision calls are not overrated.
func RequiredCapabilities(request *llm.Request) []Capability {
	if request == nil {
		return []Capability{CapabilityText}
	}
	result := make([]Capability, 0, 3)
	if request.Stream != nil && *request.Stream {
		result = append(result, CapabilityStream)
	}
	if len(request.Tools) > 0 {
		result = append(result, CapabilityTool)
	}
	vision := false
	for _, message := range request.Messages {
		for _, part := range message.Content.MultipleContent {
			if part.ImageURL != nil && strings.TrimSpace(part.ImageURL.URL) != "" {
				vision = true
				break
			}
		}
		if vision {
			break
		}
	}
	if vision {
		result = append(result, CapabilityVision)
	}
	if len(result) == 0 {
		return []Capability{CapabilityText}
	}
	return result
}
