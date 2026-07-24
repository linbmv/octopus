package selfheal

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

type Observation struct {
	HTTPStatus     int
	Headers        http.Header
	Body           []byte
	TransportError error
	DecodeError    error
}

type Diagnosis struct {
	Classification errorclass.Classification
	RootCause      model.RootCause
}

func (d Diagnosis) PatchEligible() bool {
	return d.RootCause == model.RootCauseProtocolDrift || d.RootCause == model.RootCauseWAFOrClientFingerprint
}

// Classify preserves the production ErrorLevel returned by errorclass while
// adding a diagnostic RootCause. It never changes relay retry semantics.
func Classify(observation Observation) Diagnosis {
	if observation.TransportError != nil {
		rootCause := model.RootCauseNetwork
		if !IsNetworkError(observation.TransportError) {
			rootCause = model.RootCauseUnknown
		}
		return Diagnosis{
			Classification: errorclass.Classification{Level: errorclass.ErrorLevelChannel, Reason: boundedReason(observation.TransportError.Error())},
			RootCause:      rootCause,
		}
	}
	classification := errorclass.ClassifyWithHeaders(observation.HTTPStatus, observation.Headers, observation.Body)
	if observation.DecodeError != nil {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseDecode}
	}
	if classification.Level == errorclass.ErrorLevelNone {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseNone}
	}

	body := strings.ToLower(string(limitBytes(observation.Body, 8192)))
	reason := strings.ToLower(classification.Reason)
	evidence := reason + " " + body

	if observation.HTTPStatus == http.StatusTooManyRequests || containsAny(evidence,
		"rate limit", "rate_limit", "too many requests", "quota", "resource_exhausted", "1308", "1310") {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseRateLimit}
	}
	if observation.HTTPStatus == http.StatusUnauthorized || observation.HTTPStatus == http.StatusPaymentRequired ||
		containsAny(evidence, "invalid api key", "invalid_api_key", "authentication_error", "unauthorized", "billing", "payment required") {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseAuth}
	}
	if observation.HTTPStatus == http.StatusForbidden {
		if containsAny(evidence, "cloudflare", "cf-chl-", "challenge", "captcha", "waf", "current client", "client fingerprint", "ip blocked", "enable javascript") ||
			strings.Contains(strings.ToLower(observation.Headers.Get("Content-Type")), "text/html") {
			return Diagnosis{Classification: classification, RootCause: model.RootCauseWAFOrClientFingerprint}
		}
		return Diagnosis{Classification: classification, RootCause: model.RootCauseAuth}
	}
	if strings.Contains(reason, "html/waf") || containsAny(evidence, "cloudflare", "cf-chl-", "challenge-platform", "captcha", "enable javascript") {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseWAFOrClientFingerprint}
	}
	if observation.HTTPStatus == http.StatusNotFound {
		if containsAny(evidence, "model_not_found", "model not found", "model does not exist", "invalid model", "invalid_model") {
			return Diagnosis{Classification: classification, RootCause: model.RootCauseModelAccess}
		}
		return Diagnosis{Classification: classification, RootCause: model.RootCauseEndpoint}
	}
	if observation.HTTPStatus == http.StatusBadRequest || observation.HTTPStatus == http.StatusUnprocessableEntity ||
		observation.HTTPStatus == http.StatusUnsupportedMediaType ||
		containsAny(evidence, "invalid_request", "invalid request", "validation_error", "unknown field", "unrecognized field", "missing required", "unsupported field", "schema") {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseProtocolDrift}
	}
	if observation.HTTPStatus >= 500 || containsAny(evidence,
		"overloaded", "load limit", "service unavailable", "temporarily unavailable", "server is overloaded", "服务繁忙", "负载过高") {
		return Diagnosis{Classification: classification, RootCause: model.RootCauseCapacity}
	}
	return Diagnosis{Classification: classification, RootCause: model.RootCauseUnknown}
}

func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) || containsAny(strings.ToLower(err.Error()),
		"dial tcp", "connection refused", "connection reset", "tls handshake", "no such host", "proxyconnect")
}

func boundedReason(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > 256 {
		value = string(runes[:256]) + "..."
	}
	return value
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func limitBytes(value []byte, limit int) []byte {
	if limit >= 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}
