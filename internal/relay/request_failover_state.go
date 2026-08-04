package relay

import (
	"strings"

	dbmodel "github.com/bestruirui/octopus/internal/model"
)

// requestFailoverAction describes why one request moved to its next upstream
// candidate. It is request-local state; persisted attempt actions remain the
// operator-facing audit trail.
type requestFailoverAction string

const (
	requestFailoverSelect        requestFailoverAction = "selected"
	requestFailoverRetryBaseURL  requestFailoverAction = "retry_base_url"
	requestFailoverRetryKey      requestFailoverAction = "retry_key"
	requestFailoverRetryChannel  requestFailoverAction = "retry_channel"
	requestFailoverRoutingChange requestFailoverAction = "routing_refresh"
	requestFailoverStop          requestFailoverAction = "stop"
)

type requestFailureScope uint8

const (
	requestFailureCandidate requestFailureScope = iota + 1
	requestFailureKey
	requestFailureChannel
)

// requestCandidateID contains only stable identifiers and a one-way scope
// fingerprint. Credentials and raw endpoints never enter request state logs.
type requestCandidateID struct {
	channelID        int
	channelKeyID     int
	model            string
	configVersion    int
	scopeFingerprint string
}

type requestChannelID struct {
	channelID     int
	model         string
	configVersion int
}

type requestKeyID struct {
	channelID     int
	channelKeyID  int
	model         string
	configVersion int
}

func newRequestCandidateID(channel *dbmodel.Channel, key dbmodel.ChannelKey, modelName, baseURL string) requestCandidateID {
	if channel == nil {
		return requestCandidateID{channelKeyID: key.ID, model: strings.TrimSpace(modelName)}
	}
	return requestCandidateID{
		channelID:        channel.ID,
		channelKeyID:     key.ID,
		model:            strings.TrimSpace(modelName),
		configVersion:    channel.ConfigVersion,
		scopeFingerprint: dbmodel.CapabilityScopeFingerprint(channel, key, baseURL),
	}
}

func (id requestCandidateID) channel() requestChannelID {
	return requestChannelID{channelID: id.channelID, model: id.model, configVersion: id.configVersion}
}

func (id requestCandidateID) key() requestKeyID {
	return requestKeyID{
		channelID: id.channelID, channelKeyID: id.channelKeyID,
		model: id.model, configVersion: id.configVersion,
	}
}

// requestFailoverState prevents a routing refresh from cycling through an
// unchanged failed candidate set. A changed channel ConfigVersion creates a new
// identity, so explicit administrator edits remain immediately eligible.
type requestFailoverState struct {
	exhaustedChannels   map[requestChannelID]struct{}
	exhaustedKeys       map[requestKeyID]struct{}
	exhaustedCandidates map[requestCandidateID]struct{}
	current             requestCandidateID
	hasCurrent          bool
	lastAction          requestFailoverAction
	switches            int
}

func newRequestFailoverState() *requestFailoverState {
	return &requestFailoverState{
		exhaustedChannels:   make(map[requestChannelID]struct{}),
		exhaustedKeys:       make(map[requestKeyID]struct{}),
		exhaustedCandidates: make(map[requestCandidateID]struct{}),
		lastAction:          requestFailoverSelect,
	}
}

func (s *requestFailoverState) allows(id requestCandidateID) bool {
	if s == nil {
		return true
	}
	if _, blocked := s.exhaustedChannels[id.channel()]; blocked {
		return false
	}
	if _, blocked := s.exhaustedKeys[id.key()]; blocked {
		return false
	}
	_, blocked := s.exhaustedCandidates[id]
	return !blocked
}

func (s *requestFailoverState) selectCandidate(id requestCandidateID) (requestFailoverAction, bool) {
	if s == nil {
		return requestFailoverSelect, true
	}
	if !s.allows(id) {
		return requestFailoverStop, false
	}

	action := requestFailoverSelect
	if s.hasCurrent && s.current != id {
		s.switches++
		switch {
		case s.current.channelID != id.channelID ||
			s.current.model != id.model || s.current.configVersion != id.configVersion:
			action = requestFailoverRetryChannel
		case s.current.channelKeyID != id.channelKeyID:
			action = requestFailoverRetryKey
		default:
			action = requestFailoverRetryBaseURL
		}
	}
	s.current = id
	s.hasCurrent = true
	s.lastAction = action
	return action, true
}

func (s *requestFailoverState) exhaust(id requestCandidateID, scope requestFailureScope) {
	if s == nil {
		return
	}
	switch scope {
	case requestFailureChannel:
		s.exhaustedChannels[id.channel()] = struct{}{}
		s.lastAction = requestFailoverRetryChannel
	case requestFailureKey:
		s.exhaustedKeys[id.key()] = struct{}{}
		s.lastAction = requestFailoverRetryKey
	default:
		s.exhaustedCandidates[id] = struct{}{}
		s.lastAction = requestFailoverRetryBaseURL
	}
}

func (s *requestFailoverState) routingChanged() {
	if s != nil {
		s.lastAction = requestFailoverRoutingChange
	}
}

func (s *requestFailoverState) stop() {
	if s != nil {
		s.lastAction = requestFailoverStop
	}
}

func (r *relayRun) requestFailoverState() *requestFailoverState {
	if r == nil {
		return nil
	}
	if r.failoverState == nil {
		r.failoverState = newRequestFailoverState()
	}
	return r.failoverState
}

func (r *relayRun) requestCandidateAllowed(channel *dbmodel.Channel, key dbmodel.ChannelKey, modelName, baseURL string) bool {
	state := r.requestFailoverState()
	return state == nil || state.allows(newRequestCandidateID(channel, key, modelName, baseURL))
}

func (r *relayRun) selectRequestCandidate(attempt *relayAttempt) bool {
	if r == nil || attempt == nil {
		return false
	}
	state := r.requestFailoverState()
	action, allowed := state.selectCandidate(attempt.requestCandidateID())
	if !allowed {
		return false
	}
	attempt.attemptAction = string(action)
	return true
}

func (ra *relayAttempt) requestCandidateID() requestCandidateID {
	if ra == nil {
		return requestCandidateID{}
	}
	modelName := ""
	if ra.internalRequest != nil {
		modelName = ra.internalRequest.Model
	}
	if ra.groupItem.ModelName != "" {
		modelName = ra.groupItem.ModelName
	}
	return newRequestCandidateID(ra.channel, ra.usedKey, modelName, ra.baseURL)
}

func (ra *relayAttempt) exhaustRequestCandidate(scope requestFailureScope) {
	if ra == nil || ra.relayRun == nil {
		return
	}
	ra.requestFailoverState().exhaust(ra.requestCandidateID(), scope)
}
