package selfheal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/bestruirui/octopus/internal/requestartifact"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

const (
	DefaultDiagnosticResponseBytes = 64 << 10
	MaxDiagnosticResponseBytes     = 256 << 10
)

type AttemptResult struct {
	Artifact            *requestartifact.Artifact
	ShapeDiff           []string
	HTTPStatus          int
	ResponseHeaders     map[string][]string
	Classification      Diagnosis
	ResponseFingerprint string
	Duration            time.Duration
	Success             bool
	TransportError      error `json:"-"`
}

type IsolatedExecutor struct {
	ClientForChannel func(*model.Channel) (*http.Client, error)
	MaxResponseBytes int
	Now              func() time.Time
}

func (e IsolatedExecutor) Execute(
	ctx context.Context,
	channel *model.Channel,
	key model.ChannelKey,
	endpoint string,
	internalRequest *llm.Request,
	variant Variant,
) AttemptResult {
	started := e.now()
	maxBytes := e.MaxResponseBytes
	if maxBytes <= 0 || maxBytes > MaxDiagnosticResponseBytes {
		maxBytes = DefaultDiagnosticResponseBytes
	}
	built, err := BuildVariantRequest(ctx, channel, key, endpoint, internalRequest, variant)
	if err != nil {
		return AttemptResult{Classification: Classify(Observation{TransportError: err}), Duration: e.now().Sub(started)}
	}
	clientForChannel := e.ClientForChannel
	if clientForChannel == nil {
		clientForChannel = helper.ChannelHttpClient
	}
	client, err := clientForChannel(channel)
	if err != nil {
		return AttemptResult{Artifact: built.Artifact, ShapeDiff: built.ShapeDiff, Classification: Classify(Observation{TransportError: err}), Duration: e.now().Sub(started)}
	}
	request, err := httpclient.BuildHttpRequest(ctx, built.Request)
	if err != nil {
		return AttemptResult{Artifact: built.Artifact, ShapeDiff: built.ShapeDiff, Classification: Classify(Observation{TransportError: err}), Duration: e.now().Sub(started)}
	}
	// The diagnostic client is a plain HTTP client and never enters relay's
	// balancer, circuit breaker, health score, or production log path.
	response, err := client.Do(request)
	if err != nil {
		return AttemptResult{Artifact: built.Artifact, ShapeDiff: built.ShapeDiff, Classification: Classify(Observation{TransportError: err}), Duration: e.now().Sub(started)}
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := readDiagnosticBody(ctx, response, maxBytes)
	if readErr != nil && len(body) == 0 {
		return AttemptResult{Artifact: built.Artifact, ShapeDiff: built.ShapeDiff, HTTPStatus: response.StatusCode, ResponseHeaders: model.NormalizeDiagnosticHeaders(response.Header), Classification: Classify(Observation{HTTPStatus: response.StatusCode, Headers: response.Header, TransportError: readErr}), Duration: e.now().Sub(started), TransportError: readErr}
	}
	diagnosis := Classify(Observation{HTTPStatus: response.StatusCode, Headers: response.Header, Body: body})
	if readErr != nil && diagnosis.RootCause == model.RootCauseNone {
		diagnosis = Classify(Observation{HTTPStatus: response.StatusCode, Headers: response.Header, Body: body, DecodeError: readErr})
	}
	success := isDiagnosticSuccess(response, body, diagnosis)
	return AttemptResult{
		Artifact: built.Artifact, ShapeDiff: built.ShapeDiff, HTTPStatus: response.StatusCode,
		ResponseHeaders: model.NormalizeDiagnosticHeaders(response.Header), Classification: diagnosis,
		ResponseFingerprint: fingerprintResponse(response.StatusCode, response.Header, body), Duration: e.now().Sub(started), Success: success,
	}
}

func (e IsolatedExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func readDiagnosticBody(ctx context.Context, response *http.Response, limit int) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("diagnostic upstream response has no body")
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "event-stream") {
		reader := bufio.NewReader(io.LimitReader(response.Body, int64(limit)))
		var buffer bytes.Buffer
		for buffer.Len() < limit {
			line, err := reader.ReadBytes('\n')
			buffer.Write(line)
			if bytes.Contains(buffer.Bytes(), []byte("\n\n")) || bytes.Contains(buffer.Bytes(), []byte("\r\n\r\n")) {
				return buffer.Bytes(), nil
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return buffer.Bytes(), nil
				}
				if ctx != nil && ctx.Err() != nil {
					return buffer.Bytes(), ctx.Err()
				}
				return buffer.Bytes(), err
			}
		}
		return buffer.Bytes(), fmt.Errorf("diagnostic response exceeded %d-byte limit", limit)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if len(body) > limit {
		return body[:limit], fmt.Errorf("diagnostic response exceeded %d-byte limit", limit)
	}
	return body, err
}

func isDiagnosticSuccess(response *http.Response, body []byte, diagnosis Diagnosis) bool {
	if response == nil || diagnosis.Classification.Level != errorclass.ErrorLevelNone || response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "event-stream") {
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "data:") && !strings.Contains(lower, "event: error") && !strings.Contains(lower, "\"error\"")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	return true
}

func fingerprintResponse(status int, headers http.Header, body []byte) string {
	classification := model.NormalizeDiagnosticHeaders(headers)
	encodedHeaders, _ := json.Marshal(classification)
	payload := fmt.Sprintf("%d|%s|%x", status, encodedHeaders, sha256.Sum256(boundedCopy(body, 8192)))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func boundedCopy(value []byte, max int) []byte {
	if len(value) <= max {
		return append([]byte(nil), value...)
	}
	return append([]byte(nil), value[:max]...)
}
