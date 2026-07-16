package bodylimit

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadAllAcceptsExactLimitAndRejectsOneByteMore(t *testing.T) {
	data, err := ReadAll(strings.NewReader("1234"), 4)
	if err != nil || string(data) != "1234" {
		t.Fatalf("ReadAll(exact) = (%q, %v)", data, err)
	}
	data, err = ReadAll(strings.NewReader("12345"), 4)
	if data != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadAll(oversized) = (%q, %v), want ErrTooLarge", data, err)
	}
}

func TestReaderReturnsErrorInsteadOfSilentTruncation(t *testing.T) {
	data, err := io.ReadAll(NewReader(bytes.NewBufferString("abcdef"), 3))
	if string(data) != "abc" || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bounded reader = (%q, %v), want prefix and ErrTooLarge", data, err)
	}
}

func TestReadResponseBodyRejectsDeclaredOversizeWithoutReading(t *testing.T) {
	body := &countingReadCloser{Reader: strings.NewReader("small")}
	response := &http.Response{Body: body, ContentLength: 100}
	if _, err := ReadResponseBody(response, 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadResponseBody() error = %v, want ErrTooLarge", err)
	}
	if body.reads.Load() != 0 {
		t.Fatalf("oversized declared body was read %d times", body.reads.Load())
	}
}

type countingReadCloser struct {
	io.Reader
	reads atomic.Int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.Reader.Read(p)
}

func (*countingReadCloser) Close() error { return nil }
