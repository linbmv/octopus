package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
)

// RootCause is the self-healing diagnosis vocabulary. It is intentionally
// separate from relay/errorclass.ErrorLevel, which controls production retry
// and failover behavior.
type RootCause string

const (
	RootCauseNone                   RootCause = "none"
	RootCauseCapacity               RootCause = "capacity"
	RootCauseRateLimit              RootCause = "rate_limit"
	RootCauseAuth                   RootCause = "auth"
	RootCauseWAFOrClientFingerprint RootCause = "waf_or_client_fingerprint"
	RootCauseProtocolDrift          RootCause = "protocol_drift"
	RootCauseEndpoint               RootCause = "endpoint"
	RootCauseNetwork                RootCause = "network"
	RootCauseDecode                 RootCause = "decode"
	RootCauseModelAccess            RootCause = "model_access"
	RootCauseUnknown                RootCause = "unknown"
)

func (r RootCause) Valid() bool {
	switch r {
	case RootCauseNone, RootCauseCapacity, RootCauseRateLimit, RootCauseAuth,
		RootCauseWAFOrClientFingerprint, RootCauseProtocolDrift, RootCauseEndpoint,
		RootCauseNetwork, RootCauseDecode, RootCauseModelAccess, RootCauseUnknown:
		return true
	default:
		return false
	}
}

type DiagnosticMode string

const (
	DiagnosticModePreview DiagnosticMode = "preview"
	DiagnosticModeLive    DiagnosticMode = "live"
	DiagnosticModeCompare DiagnosticMode = "compare"
)

func (m DiagnosticMode) Valid() bool {
	return m == DiagnosticModePreview || m == DiagnosticModeLive || m == DiagnosticModeCompare
}

type DiagnosticTrigger string

const (
	DiagnosticTriggerManual   DiagnosticTrigger = "manual"
	DiagnosticTriggerSentinel DiagnosticTrigger = "sentinel"
	DiagnosticTriggerFailure  DiagnosticTrigger = "failure"
)

func (t DiagnosticTrigger) Valid() bool {
	return t == DiagnosticTriggerManual || t == DiagnosticTriggerSentinel || t == DiagnosticTriggerFailure
}

type DiagnosticSessionStatus string

const (
	DiagnosticSessionQueued    DiagnosticSessionStatus = "queued"
	DiagnosticSessionRunning   DiagnosticSessionStatus = "running"
	DiagnosticSessionCompleted DiagnosticSessionStatus = "completed"
	DiagnosticSessionFailed    DiagnosticSessionStatus = "failed"
	DiagnosticSessionCanceled  DiagnosticSessionStatus = "canceled"
	DiagnosticSessionExpired   DiagnosticSessionStatus = "expired"
)

func (s DiagnosticSessionStatus) Valid() bool {
	switch s {
	case DiagnosticSessionQueued, DiagnosticSessionRunning, DiagnosticSessionCompleted,
		DiagnosticSessionFailed, DiagnosticSessionCanceled, DiagnosticSessionExpired:
		return true
	default:
		return false
	}
}

type DiagnosticAttemptStatus string

const (
	DiagnosticAttemptPending DiagnosticAttemptStatus = "pending"
	DiagnosticAttemptRunning DiagnosticAttemptStatus = "running"
	DiagnosticAttemptSuccess DiagnosticAttemptStatus = "success"
	DiagnosticAttemptFailed  DiagnosticAttemptStatus = "failed"
	DiagnosticAttemptSkipped DiagnosticAttemptStatus = "skipped"
)

func (s DiagnosticAttemptStatus) Valid() bool {
	switch s {
	case DiagnosticAttemptPending, DiagnosticAttemptRunning, DiagnosticAttemptSuccess,
		DiagnosticAttemptFailed, DiagnosticAttemptSkipped:
		return true
	default:
		return false
	}
}

type PatchConfidence string

const (
	PatchConfidenceHigh   PatchConfidence = "high"
	PatchConfidenceMedium PatchConfidence = "medium"
	PatchConfidenceLow    PatchConfidence = "low"
)

func (c PatchConfidence) Valid() bool {
	return c == PatchConfidenceHigh || c == PatchConfidenceMedium || c == PatchConfidenceLow
}

type ChannelPatchStatus string

const (
	ChannelPatchPreviewed      ChannelPatchStatus = "previewed"
	ChannelPatchApplying       ChannelPatchStatus = "applying"
	ChannelPatchApplied        ChannelPatchStatus = "applied"
	ChannelPatchRolledBack     ChannelPatchStatus = "rolled_back"
	ChannelPatchRollbackFailed ChannelPatchStatus = "rollback_failed"
	ChannelPatchRejected       ChannelPatchStatus = "rejected"
)

func (s ChannelPatchStatus) Valid() bool {
	switch s {
	case ChannelPatchPreviewed, ChannelPatchApplying, ChannelPatchApplied,
		ChannelPatchRolledBack, ChannelPatchRollbackFailed, ChannelPatchRejected:
		return true
	default:
		return false
	}
}

// DiagnosticSession is runtime evidence and is intentionally excluded from
// backup exports. ChannelKeyID is an identifier only; the secret never enters
// this table or the API representation.
type DiagnosticSession struct {
	ID                  string                  `json:"id" gorm:"primaryKey;size:64"`
	ChannelID           int                     `json:"channel_id" gorm:"not null;index:idx_diagnostic_session_scope,priority:1"`
	ChannelKeyID        int                     `json:"channel_key_id" gorm:"not null;index:idx_diagnostic_session_scope,priority:2"`
	Model               string                  `json:"model" gorm:"size:256;not null;index:idx_diagnostic_session_scope,priority:3"`
	WireProtocol        llm.APIFormat           `json:"wire_protocol" gorm:"size:64;not null;index:idx_diagnostic_session_scope,priority:4"`
	Endpoint            string                  `json:"endpoint,omitempty" gorm:"size:2048;not null"`
	EndpointFingerprint string                  `json:"endpoint_fingerprint" gorm:"size:64;not null;index:idx_diagnostic_session_scope,priority:5"`
	ScopeFingerprint    string                  `json:"scope_fingerprint" gorm:"size:64;not null;index:idx_diagnostic_session_scope,priority:6"`
	ConfigVersion       int                     `json:"config_version" gorm:"not null"`
	Mode                DiagnosticMode          `json:"mode" gorm:"size:16;not null"`
	Trigger             DiagnosticTrigger       `json:"trigger" gorm:"size:16;not null"`
	Status              DiagnosticSessionStatus `json:"status" gorm:"size:16;not null;index"`
	ActiveKey           *string                 `json:"-" gorm:"size:64;uniqueIndex"`
	RootCause           RootCause               `json:"root_cause" gorm:"size:48;not null;index"`
	ErrorLevel          string                  `json:"error_level,omitempty" gorm:"size:16"`
	ErrorReason         string                  `json:"error_reason,omitempty" gorm:"size:512"`
	Actor               string                  `json:"actor,omitempty" gorm:"size:128"`
	MaxAttempts         int                     `json:"max_attempts" gorm:"not null"`
	AttemptCount        int                     `json:"attempt_count" gorm:"not null;default:0"`
	ReservedCostUSD     float64                 `json:"reserved_cost_usd" gorm:"not null;default:0"`
	SpentCostUSD        float64                 `json:"spent_cost_usd" gorm:"not null;default:0"`
	StopReason          string                  `json:"stop_reason,omitempty" gorm:"size:512"`
	Deadline            time.Time               `json:"deadline" gorm:"not null;index"`
	StartedAt           *time.Time              `json:"started_at,omitempty"`
	CompletedAt         *time.Time              `json:"completed_at,omitempty"`
	CreatedAt           time.Time               `json:"created_at" gorm:"index"`
	UpdatedAt           time.Time               `json:"updated_at"`
}

// DiagnosticAttempt stores only bounded response evidence. Headers are
// allowlisted and values are never used for Authorization or cookie fields.
type DiagnosticAttempt struct {
	ID                  uint                     `json:"id" gorm:"primaryKey"`
	SessionID           string                   `json:"session_id" gorm:"size:64;not null;index"`
	VariantID           string                   `json:"variant_id" gorm:"size:64;not null"`
	ParentVariantID     string                   `json:"parent_variant_id,omitempty" gorm:"size:64"`
	ChangedDimension    string                   `json:"changed_dimension,omitempty" gorm:"size:64"`
	Status              DiagnosticAttemptStatus  `json:"status" gorm:"size:16;not null;index"`
	RequestShape        requestartifact.Artifact `json:"request_shape" gorm:"serializer:json;not null"`
	ResponseHeaders     map[string][]string      `json:"response_headers,omitempty" gorm:"serializer:json"`
	HTTPStatus          int                      `json:"http_status,omitempty"`
	ErrorLevel          string                   `json:"error_level,omitempty" gorm:"size:16"`
	RootCause           RootCause                `json:"root_cause" gorm:"size:48;not null;index"`
	ErrorReason         string                   `json:"error_reason,omitempty" gorm:"size:512"`
	ShapeDiff           []string                 `json:"shape_diff,omitempty" gorm:"serializer:json"`
	ResponseFingerprint string                   `json:"response_fingerprint,omitempty" gorm:"size:64"`
	Success             bool                     `json:"success" gorm:"not null;default:false"`
	DurationMS          int64                    `json:"duration_ms,omitempty"`
	CostUSD             float64                  `json:"cost_usd" gorm:"not null;default:0"`
	StartedAt           time.Time                `json:"started_at" gorm:"not null;index"`
	FinishedAt          *time.Time               `json:"finished_at,omitempty"`
	CreatedAt           time.Time                `json:"created_at"`
}

type ChannelPatchChange struct {
	Field              string          `json:"field"`
	Before             json.RawMessage `json:"before"`
	After              json.RawMessage `json:"after"`
	EvidenceVariantIDs []string        `json:"evidence_variant_ids,omitempty"`
}

type ChannelPatch struct {
	ID                              string               `json:"id" gorm:"primaryKey;size:64"`
	ChannelID                       int                  `json:"channel_id" gorm:"not null;index"`
	DiagnosticSessionID             string               `json:"diagnostic_session_id" gorm:"size:64;not null;index"`
	ExpectedScopeFingerprint        string               `json:"expected_scope_fingerprint" gorm:"size:64;not null"`
	BaseChannelVersion              int                  `json:"base_channel_version" gorm:"not null"`
	Confidence                      PatchConfidence      `json:"confidence" gorm:"size:16;not null"`
	Changes                         []ChannelPatchChange `json:"changes" gorm:"serializer:json;not null"`
	BeforeSnapshot                  ChannelPatchSnapshot `json:"before_snapshot" gorm:"serializer:json;not null"`
	AfterSnapshot                   ChannelPatchSnapshot `json:"after_snapshot" gorm:"serializer:json;not null"`
	VerificationModel               string               `json:"verification_model" gorm:"size:256;not null"`
	VerificationEndpointFingerprint string               `json:"verification_endpoint_fingerprint" gorm:"size:64;not null"`
	MaxLiveRequests                 int                  `json:"max_live_requests" gorm:"not null;default:1"`
	Status                          ChannelPatchStatus   `json:"status" gorm:"size:16;not null;index"`
	ApplyError                      string               `json:"apply_error,omitempty" gorm:"size:512"`
	VerificationHTTPStatus          int                  `json:"verification_http_status,omitempty"`
	VerificationErrorLevel          string               `json:"verification_error_level,omitempty" gorm:"size:16"`
	VerificationRootCause           RootCause            `json:"verification_root_cause,omitempty" gorm:"size:48"`
	VerificationReason              string               `json:"verification_reason,omitempty" gorm:"size:512"`
	VerificationFingerprint         string               `json:"verification_fingerprint,omitempty" gorm:"size:64"`
	VerifiedAt                      *time.Time           `json:"verified_at,omitempty"`
	CreatedAt                       time.Time            `json:"created_at" gorm:"index"`
	UpdatedAt                       time.Time            `json:"updated_at"`
}

// ChannelPatchSnapshot contains only patchable channel settings. It is safe
// to persist and return to an administrator because it has no key material or
// proxy credentials.
type ChannelPatchSnapshot struct {
	UserAgent        string            `json:"user_agent"`
	CustomHeader     []CustomHeader    `json:"custom_header,omitempty"`
	HeaderRules      []HeaderRule      `json:"header_rules,omitempty"`
	JSONRewriteRules []JSONRewriteRule `json:"json_rewrite_rules,omitempty"`
	ParamOverride    *string           `json:"param_override,omitempty"`
	RawPassthrough   bool              `json:"raw_passthrough"`
}

func (c *ChannelPatchSnapshot) Valid() bool {
	return c != nil && len(c.UserAgent) <= MaxHeaderValueBytes &&
		len(c.CustomHeader) <= MaxCustomHeaders && len(c.HeaderRules) <= MaxHeaderRules &&
		len(c.JSONRewriteRules) <= MaxJSONRewriteRules &&
		(c.ParamOverride == nil || len(*c.ParamOverride) <= MaxParamOverrideBytes)
}

func NewChannelPatchSnapshot(channel *Channel) ChannelPatchSnapshot {
	if channel == nil {
		return ChannelPatchSnapshot{}
	}
	return ChannelPatchSnapshot{
		UserAgent: channel.UserAgent, CustomHeader: append([]CustomHeader(nil), channel.CustomHeader...),
		HeaderRules: append([]HeaderRule(nil), channel.HeaderRules...), JSONRewriteRules: append([]JSONRewriteRule(nil), channel.JSONRewriteRules...),
		ParamOverride: cloneStringPointer(channel.ParamOverride), RawPassthrough: channel.RawPassthrough,
	}
}

func (s ChannelPatchSnapshot) Apply(channel *Channel) {
	if channel == nil {
		return
	}
	channel.UserAgent = s.UserAgent
	channel.CustomHeader = append([]CustomHeader(nil), s.CustomHeader...)
	channel.HeaderRules = append([]HeaderRule(nil), s.HeaderRules...)
	channel.JSONRewriteRules = append([]JSONRewriteRule(nil), s.JSONRewriteRules...)
	channel.ParamOverride = cloneStringPointer(s.ParamOverride)
	channel.RawPassthrough = s.RawPassthrough
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (s DiagnosticSession) Valid() bool {
	return strings.TrimSpace(s.ID) != "" && s.ChannelID > 0 && s.ChannelKeyID > 0 &&
		strings.TrimSpace(s.Model) != "" && s.WireProtocol != "" && strings.TrimSpace(s.Endpoint) != "" &&
		s.EndpointFingerprint != "" && s.ScopeFingerprint != "" && s.ConfigVersion > 0 &&
		s.Mode.Valid() && s.Trigger.Valid() && s.Status.Valid() && s.RootCause.Valid() &&
		s.MaxAttempts >= 1 && s.MaxAttempts <= 16 && !s.Deadline.IsZero()
}

func (a DiagnosticAttempt) Valid() bool {
	return strings.TrimSpace(a.SessionID) != "" && strings.TrimSpace(a.VariantID) != "" &&
		a.Status.Valid() && a.RootCause.Valid() && len(a.ErrorReason) <= 512 && a.DurationMS >= 0 && a.CostUSD >= 0
}

func (p ChannelPatch) Valid() bool {
	return p.ChannelID > 0 && strings.TrimSpace(p.DiagnosticSessionID) != "" &&
		p.ExpectedScopeFingerprint != "" && p.BaseChannelVersion > 0 && p.Confidence.Valid() &&
		len(p.Changes) > 0 && len(p.Changes) <= 16 && strings.TrimSpace(p.VerificationModel) != "" &&
		p.VerificationEndpointFingerprint != "" && p.MaxLiveRequests == 1 && p.Status.Valid() &&
		p.BeforeSnapshot.Valid() && p.AfterSnapshot.Valid()
}

func (p *ChannelPatch) Normalize() {
	if p == nil {
		return
	}
	p.ID = strings.TrimSpace(p.ID)
	p.DiagnosticSessionID = strings.TrimSpace(p.DiagnosticSessionID)
	p.ExpectedScopeFingerprint = strings.TrimSpace(p.ExpectedScopeFingerprint)
	p.VerificationModel = strings.TrimSpace(p.VerificationModel)
	p.VerificationEndpointFingerprint = strings.TrimSpace(p.VerificationEndpointFingerprint)
	p.ApplyError = boundedDiagnosticReason(p.ApplyError)
	for i := range p.Changes {
		p.Changes[i].Field = strings.TrimSpace(p.Changes[i].Field)
		p.Changes[i].EvidenceVariantIDs = boundedVariantIDs(p.Changes[i].EvidenceVariantIDs)
	}
}

func NormalizeDiagnosticHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	allow := map[string]struct{}{
		"content-type": {}, "retry-after": {}, "x-ratelimit-scope": {}, "x-ratelimit-limit": {},
		"x-ratelimit-remaining": {}, "x-request-id": {}, "server": {}, "content-encoding": {},
	}
	result := make(map[string][]string)
	for name, values := range headers {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, ok := allow[name]; !ok {
			continue
		}
		copyValues := make([]string, 0, len(values))
		for _, value := range values {
			value = boundedDiagnosticReason(value)
			if value != "" {
				copyValues = append(copyValues, value)
			}
		}
		if len(copyValues) > 0 {
			result[name] = copyValues
		}
	}
	return result
}

func boundedDiagnosticReason(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, value))
	if len([]rune(value)) > 256 {
		return string([]rune(value)[:256]) + "..."
	}
	return value
}

func boundedVariantIDs(ids []string) []string {
	if len(ids) > 16 {
		ids = ids[:16]
	}
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 64 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// ChannelConfigFingerprint covers all channel settings relevant to a patch
// without serializing keys, runtime statuses, or credentials.
func ChannelConfigFingerprint(channel *Channel) string {
	if channel == nil {
		return ""
	}
	payload := struct {
		ID                                        int
		Name, Type, Model, CustomModel, UserAgent string
		Enabled, SelfHealingEnabled               bool
		BaseUrls                                  []BaseUrl
		Proxy                                     bool
		CustomHeader                              []CustomHeader
		HeaderRules                               []HeaderRule
		JSONRewriteRules                          []JSONRewriteRule
		ParamOverride                             *string
		RawPassthrough                            bool
		ChannelProxy                              *string
		PolicyProfile                             ChannelPolicyProfile
	}{channel.ID, channel.Name, string(channel.Type), channel.Model, channel.CustomModel, channel.UserAgent,
		channel.Enabled, channel.SelfHealingEnabled,
		append([]BaseUrl(nil), channel.BaseUrls...), channel.Proxy, append([]CustomHeader(nil), channel.CustomHeader...),
		append([]HeaderRule(nil), channel.HeaderRules...), append([]JSONRewriteRule(nil), channel.JSONRewriteRules...),
		cloneStringPointer(channel.ParamOverride), channel.RawPassthrough, cloneStringPointer(channel.ChannelProxy), channel.PolicyProfile}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s DiagnosticSession) Normalize() DiagnosticSession {
	s.ErrorReason = boundedDiagnosticReason(s.ErrorReason)
	s.StopReason = boundedDiagnosticReason(s.StopReason)
	s.Actor = boundedDiagnosticReason(s.Actor)
	return s
}

func (a *DiagnosticAttempt) Normalize() {
	if a == nil {
		return
	}
	a.SessionID = strings.TrimSpace(a.SessionID)
	a.VariantID = strings.TrimSpace(a.VariantID)
	a.ParentVariantID = strings.TrimSpace(a.ParentVariantID)
	a.ChangedDimension = strings.TrimSpace(a.ChangedDimension)
	a.ErrorReason = boundedDiagnosticReason(a.ErrorReason)
	a.ShapeDiff = boundedVariantIDs(a.ShapeDiff)
	a.ResponseHeaders = normalizeStoredHeaders(a.ResponseHeaders)
	if a.ResponseFingerprint == "" && len(a.ResponseHeaders) > 0 {
		encoded, _ := json.Marshal(a.ResponseHeaders)
		sum := sha256.Sum256(encoded)
		a.ResponseFingerprint = hex.EncodeToString(sum[:])
	}
}

func normalizeStoredHeaders(headers map[string][]string) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string][]string, len(headers))
	for name, values := range headers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		clean := make([]string, 0, len(values))
		for _, value := range values {
			if value = boundedDiagnosticReason(value); value != "" {
				clean = append(clean, value)
			}
		}
		if len(clean) > 0 {
			result[name] = clean
		}
	}
	return result
}
