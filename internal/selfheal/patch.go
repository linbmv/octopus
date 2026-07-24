package selfheal

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/requestrewrite"
)

var ErrNoPatchCandidate = errors.New("diagnostic evidence does not support a patch")

func GenerateCandidatePatch(
	session *model.DiagnosticSession,
	channel *model.Channel,
	plan VariantPlan,
	attempts []model.DiagnosticAttempt,
) (*model.ChannelPatch, error) {
	if session == nil || channel == nil || session.ChannelID != channel.ID {
		return nil, fmt.Errorf("%w: invalid diagnostic patch scope", ErrNoPatchCandidate)
	}
	variants := make(map[string]Variant, len(plan.Variants))
	for _, variant := range plan.Variants {
		variants[variant.ID] = variant
	}
	baselineFailed := false
	for _, attempt := range attempts {
		variant, ok := variants[attempt.VariantID]
		if ok && variant.Dimension == DimensionBaseline && !attempt.Success {
			baselineFailed = true
			break
		}
	}
	if !baselineFailed {
		return nil, ErrNoPatchCandidate
	}
	var candidate *model.DiagnosticAttempt
	for i := range attempts {
		variant, ok := variants[attempts[i].VariantID]
		if !ok || !attempts[i].Success || variant.Dimension == DimensionBaseline {
			continue
		}
		candidate = &attempts[i]
		break
	}
	if candidate == nil {
		return nil, ErrNoPatchCandidate
	}
	variant := variants[candidate.VariantID]
	before := model.NewChannelPatchSnapshot(channel)
	after := before
	if err := applyVariantToSnapshot(&after, variant); err != nil {
		return nil, err
	}
	if err := validatePatchSnapshotForChannel(after, channel); err != nil {
		return nil, fmt.Errorf("%w: generated patch is incompatible with channel: %v", ErrNoPatchCandidate, err)
	}
	changes, err := diffPatchSnapshots(before, after, []string{candidate.VariantID})
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, ErrNoPatchCandidate
	}
	confidence := ComputeConfidence(variant, attempts)
	patch := &model.ChannelPatch{
		ChannelID: channel.ID, DiagnosticSessionID: session.ID,
		ExpectedScopeFingerprint: session.ScopeFingerprint, BaseChannelVersion: session.ConfigVersion,
		Confidence: confidence, Changes: changes, BeforeSnapshot: before, AfterSnapshot: after,
		VerificationModel: session.Model, VerificationEndpointFingerprint: session.EndpointFingerprint,
		MaxLiveRequests: 1, Status: model.ChannelPatchPreviewed,
	}
	patch.Normalize()
	if !patch.Valid() {
		return nil, fmt.Errorf("%w: generated patch failed validation", ErrNoPatchCandidate)
	}
	return patch, nil
}

func applyVariantToSnapshot(snapshot *model.ChannelPatchSnapshot, variant Variant) error {
	if snapshot == nil {
		return ErrNoPatchCandidate
	}
	variant.Normalize()
	switch variant.Dimension {
	case DimensionUserAgent:
		if variant.UserAgent == nil {
			return ErrNoPatchCandidate
		}
		snapshot.UserAgent = *variant.UserAgent
	case DimensionHeader:
		for _, name := range variant.HeaderRemove {
			if requestrewrite.IsProtectedHeader(name) {
				return fmt.Errorf("protected header %q is not patchable", name)
			}
			removeSnapshotHeader(snapshot, name)
			snapshot.HeaderRules = append(snapshot.HeaderRules, model.HeaderRule{Action: "remove", HeaderKey: name})
		}
		names := make([]string, 0, len(variant.HeaderSet))
		for name := range variant.HeaderSet {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if requestrewrite.IsProtectedHeader(name) {
				return fmt.Errorf("protected header %q is not patchable", name)
			}
			removeSnapshotHeader(snapshot, name)
			snapshot.HeaderRules = append(snapshot.HeaderRules, model.HeaderRule{Action: "set", HeaderKey: name, HeaderValue: variant.HeaderSet[name]})
		}
	case DimensionBody:
		fields := make([]string, 0, len(variant.BodySet))
		for name := range variant.BodySet {
			fields = append(fields, name)
		}
		sort.Strings(fields)
		for _, name := range fields {
			if !safeTopLevelField(name) {
				return fmt.Errorf("body field %q is not patchable", name)
			}
			value, err := json.Marshal(variant.BodySet[name])
			if err != nil {
				return fmt.Errorf("encode body patch %s: %w", name, err)
			}
			encoded := string(value)
			snapshot.JSONRewriteRules = append(snapshot.JSONRewriteRules, model.JSONRewriteRule{Action: "override", Path: "/" + name, Value: &encoded})
		}
		for _, name := range variant.BodyDelete {
			if !safeTopLevelField(name) {
				return fmt.Errorf("body field %q is not patchable", name)
			}
			snapshot.JSONRewriteRules = append(snapshot.JSONRewriteRules, model.JSONRewriteRule{Action: "remove", Path: "/" + name})
		}
	default:
		return ErrNoPatchCandidate
	}
	return validatePatchSnapshot(*snapshot)
}

func removeSnapshotHeader(snapshot *model.ChannelPatchSnapshot, name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	snapshot.CustomHeader = slices.DeleteFunc(snapshot.CustomHeader, func(header model.CustomHeader) bool {
		return strings.EqualFold(strings.TrimSpace(header.HeaderKey), name)
	})
	snapshot.HeaderRules = slices.DeleteFunc(snapshot.HeaderRules, func(rule model.HeaderRule) bool {
		return strings.EqualFold(strings.TrimSpace(rule.HeaderKey), name)
	})
}

func safeTopLevelField(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/~") {
		return false
	}
	switch name {
	case "stream", "store", "reasoning", "include", "instructions", "thinking":
		return true
	default:
		return false
	}
}

func validatePatchSnapshot(snapshot model.ChannelPatchSnapshot) error {
	if !snapshot.Valid() {
		return errors.New("patch snapshot exceeds model limits")
	}
	channel := &model.Channel{
		Name: "patch-validation", Type: "openai/responses", Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://example.invalid"}}, Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "placeholder"}},
		UserAgent: snapshot.UserAgent, CustomHeader: snapshot.CustomHeader, HeaderRules: snapshot.HeaderRules,
		JSONRewriteRules: snapshot.JSONRewriteRules, ParamOverride: snapshot.ParamOverride, RawPassthrough: snapshot.RawPassthrough,
	}
	return model.ValidateChannel(channel)
}

func validatePatchSnapshotForChannel(snapshot model.ChannelPatchSnapshot, channel *model.Channel) error {
	if channel == nil {
		return errors.New("channel is required")
	}
	if !snapshot.Valid() {
		return errors.New("patch snapshot exceeds model limits")
	}
	copyChannel := *channel
	snapshot.Apply(&copyChannel)
	return model.ValidateChannel(&copyChannel)
}

func diffPatchSnapshots(before, after model.ChannelPatchSnapshot, evidence []string) ([]model.ChannelPatchChange, error) {
	changes := make([]model.ChannelPatchChange, 0, 6)
	appendChange := func(field string, left, right any) error {
		leftJSON, err := json.Marshal(left)
		if err != nil {
			return err
		}
		rightJSON, err := json.Marshal(right)
		if err != nil {
			return err
		}
		if string(leftJSON) == string(rightJSON) {
			return nil
		}
		changes = append(changes, model.ChannelPatchChange{Field: field, Before: leftJSON, After: rightJSON, EvidenceVariantIDs: append([]string(nil), evidence...)})
		return nil
	}
	for _, item := range []struct {
		field       string
		left, right any
	}{
		{"user_agent", before.UserAgent, after.UserAgent},
		{"custom_header", before.CustomHeader, after.CustomHeader},
		{"header_rules", before.HeaderRules, after.HeaderRules},
		{"json_rewrite_rules", before.JSONRewriteRules, after.JSONRewriteRules},
		{"param_override", before.ParamOverride, after.ParamOverride},
		{"raw_passthrough", before.RawPassthrough, after.RawPassthrough},
	} {
		if err := appendChange(item.field, item.left, item.right); err != nil {
			return nil, err
		}
	}
	return changes, nil
}
