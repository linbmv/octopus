package bodylimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const DefaultMetadataResponseBytes int64 = 16 << 20

var ErrTooLarge = errors.New("body exceeds configured size limit")

type bufferedBodyContextKey struct{}

func WithBufferedBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, bufferedBodyContextKey{}, body)
}

func BufferedBody(ctx context.Context) ([]byte, bool) {
	if ctx == nil {
		return nil, false
	}
	body, ok := ctx.Value(bufferedBodyContextKey{}).([]byte)
	return body, ok
}

type TooLargeError struct {
	Limit int64
}

func (e *TooLargeError) Error() string {
	if e == nil {
		return ErrTooLarge.Error()
	}
	return fmt.Sprintf("%s (%d bytes)", ErrTooLarge, e.Limit)
}

func (e *TooLargeError) Is(target error) bool {
	return target == ErrTooLarge
}

// Reader exposes at most limit bytes. It probes one byte beyond the boundary
// and returns TooLargeError instead of silently turning an oversized body into
// a valid truncated payload.
type Reader struct {
	reader    io.Reader
	remaining int64
	limit     int64
	exceeded  bool
}

func NewReader(reader io.Reader, limit int64) *Reader {
	return &Reader{reader: reader, remaining: limit, limit: limit}
}

func (r *Reader) Read(p []byte) (int, error) {
	if r == nil || r.reader == nil {
		return 0, io.EOF
	}
	if r.limit < 0 {
		return 0, &TooLargeError{Limit: r.limit}
	}
	if r.exceeded {
		return 0, &TooLargeError{Limit: r.limit}
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			r.exceeded = true
			return 0, &TooLargeError{Limit: r.limit}
		}
		return 0, err
	}

	readBuffer := p
	if int64(len(readBuffer)) > r.remaining+1 {
		readBuffer = readBuffer[:r.remaining+1]
	}
	n, err := r.reader.Read(readBuffer)
	if int64(n) > r.remaining {
		allowed := int(r.remaining)
		r.remaining = 0
		r.exceeded = true
		return allowed, &TooLargeError{Limit: r.limit}
	}
	r.remaining -= int64(n)
	return n, err
}

type readCloser struct {
	*Reader
	closer io.Closer
}

func NewReadCloser(body io.ReadCloser, limit int64) io.ReadCloser {
	return &readCloser{Reader: NewReader(body, limit), closer: body}
}

func (r *readCloser) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func ReadAll(reader io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, &TooLargeError{Limit: limit}
	}
	data, err := io.ReadAll(NewReader(reader, limit))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func ReadResponseBody(response *http.Response, limit int64) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("response body is missing")
	}
	if response.ContentLength > limit {
		return nil, &TooLargeError{Limit: limit}
	}
	return ReadAll(response.Body, limit)
}
