package relay

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bestruirui/octopus/internal/utils/bodylimit"
)

// responseLimitTransport bounds every non-streaming response and HTTP error
// body. Successful streams are not byte-capped; their body is only observed for
// activity so long-lived SSE/binary streams remain unlimited.
type responseLimitTransport struct {
	base       http.RoundTripper
	maxBytes   int64
	limitAll   bool
	onActivity func()
	onResponse func()
}

func (t *responseLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil && t.onResponse != nil {
		// 响应头已到达：per-attempt 守卫在此停表，body 读取阶段不再受其约束。
		t.onResponse()
	}
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if t.limitAll || response.StatusCode >= http.StatusBadRequest {
		if t.maxBytes <= 0 || response.ContentLength > t.maxBytes {
			closeErr := response.Body.Close()
			return nil, fmt.Errorf("upstream response body too large: %w", errors.Join(&bodylimit.TooLargeError{Limit: t.maxBytes}, closeErr))
		}
		response.Body = bodylimit.NewReadCloser(response.Body, t.maxBytes)
		return response, nil
	}
	if t.onActivity != nil {
		response.Body = &activityReadCloser{ReadCloser: response.Body, onActivity: t.onActivity}
	}
	return response, nil
}

func httpClientWithResponseLimit(client *http.Client, maxBytes int64, limitAll bool, onActivity func(), onResponse func()) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &responseLimitTransport{
		base:       base,
		maxBytes:   maxBytes,
		limitAll:   limitAll,
		onActivity: onActivity,
		onResponse: onResponse,
	}
	return &clone
}

type activityReadCloser struct {
	io.ReadCloser
	onActivity func()
}

func (r *activityReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.onActivity != nil {
		r.onActivity()
	}
	return n, err
}

func newStreamActivitySignal() (<-chan struct{}, func()) {
	activity := make(chan struct{}, 1)
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	return activity, touch
}
