package httpx

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	// StreamRetry is the reconnect delay announced in the first frame of every stream
	// (fastify-sse-v2's default).
	StreamRetry = 3 * time.Second
	// streamWriteTimeout bounds one frame write to a client that stopped reading.
	streamWriteTimeout = 10 * time.Second
)

// Stream is an open Server-Sent-Events response. It is used by one goroutine at a time.
type Stream struct {
	w  http.ResponseWriter // the underlying writer, below Echo's wrapper
	rc *http.ResponseController
}

// StreamEvent is one SSE frame. Empty fields are left out, as fastify-sse-v2 does.
type StreamEvent struct {
	ID   string
	Name string
	Data string
}

// OpenStream turns the response of c into an SSE stream, byte-compatible with fastify-sse-v2@4.2.2:
// status 200, the stream headers on top of those the middleware already set, then the first
// frame "retry: 3000". It also lifts the server's per-connection deadlines for this request:
// the Echo server sets ReadTimeout 30 s, which would otherwise cancel the request context of a
// stream that lives longer. It must be called before anything is written to the response.
func OpenStream(c *echo.Context) (*Stream, error) {
	resp, err := echo.UnwrapResponse(c.Response())
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	if resp.Committed {
		return nil, errors.New("open stream: response already committed")
	}
	rc := http.NewResponseController(resp.ResponseWriter)
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, fmt.Errorf("open stream: clear read deadline: %w", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, fmt.Errorf("open stream: clear write deadline: %w", err)
	}

	h := resp.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Connection", "keep-alive")
	h.Set("Cache-Control", "no-cache,no-transform")
	h.Set("x-no-compression", "1")
	resp.WriteHeader(http.StatusOK)

	s := &Stream{w: resp, rc: rc}
	if err := s.write([]byte("retry: " + strconv.FormatInt(StreamRetry.Milliseconds(), 10) + "\n\n")); err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
}

// Event writes one frame, "id: …\nevent: …\ndata: …\n\n", and flushes it. A multi-line Data
// becomes one data field per line. The error is that of the connection; the caller stops.
func (s *Stream) Event(ev StreamEvent) error {
	if strings.ContainsAny(ev.ID, "\r\n") || strings.ContainsAny(ev.Name, "\r\n") {
		return errors.New("stream event: id and name must be single-line")
	}
	var b bytes.Buffer
	if ev.ID != "" {
		b.WriteString("id: " + ev.ID + "\n")
	}
	if ev.Name != "" {
		b.WriteString("event: " + ev.Name + "\n")
	}
	if ev.Data != "" {
		data := strings.ReplaceAll(strings.ReplaceAll(ev.Data, "\r\n", "\n"), "\r", "\n")
		for line := range strings.SplitSeq(data, "\n") {
			b.WriteString("data: " + line + "\n")
		}
	}
	if b.Len() == 0 {
		return nil
	}
	b.WriteByte('\n')
	return s.write(b.Bytes())
}

// write sends p and flushes, under a deadline of its own that is lifted again afterwards, so
// that it cannot leak into the next request on a kept-alive connection.
func (s *Stream) write(p []byte) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	_, err := s.w.Write(p)
	if err == nil {
		err = s.rc.Flush()
	}
	if derr := s.rc.SetWriteDeadline(time.Time{}); err == nil && derr != nil && !errors.Is(derr, http.ErrNotSupported) {
		err = derr
	}
	return err
}
