package relay

import (
	"bytes"
	"net/http"
	"testing"
)

func TestUpstreamFailureObserverBoundsEvidenceAndDropsSensitiveHeaders(t *testing.T) {
	observed := make(chan UpstreamFailureObservation, 1)
	remove := RegisterUpstreamFailureObserver(func(observation UpstreamFailureObservation) { observed <- observation })
	defer remove()
	observeUpstreamFailure(UpstreamFailureObservation{
		ChannelID: 3, ChannelKeyID: 4, Model: "model-a", Endpoint: "https://provider.test",
		HTTPStatus: http.StatusForbidden,
		Headers: http.Header{
			"Content-Type": {"text/html"}, "Retry-After": {"1"},
			"Authorization": {"Bearer secret"}, "Set-Cookie": {"session=secret"},
		},
		ResponseBody: bytes.Repeat([]byte("x"), maxFailureObservationBodyBytes+100),
	})
	got := <-observed
	if len(got.ResponseBody) != maxFailureObservationBodyBytes {
		t.Fatalf("observed response bytes = %d", len(got.ResponseBody))
	}
	if got.Headers.Get("Content-Type") != "text/html" || got.Headers.Get("Retry-After") != "1" {
		t.Fatalf("bounded headers = %#v", got.Headers)
	}
	if got.Headers.Get("Authorization") != "" || got.Headers.Get("Set-Cookie") != "" {
		t.Fatalf("observer retained sensitive headers: %#v", got.Headers)
	}
}
