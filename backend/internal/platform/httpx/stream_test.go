package httpx_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

const (
	// shortReadTimeout stands in for Echo v5.4.0's hard-coded ReadTimeout of 30 s
	// (echo/v5@v5.4.0/server.go:113), shrunk so that the test can outlive it.
	shortReadTimeout = 300 * time.Millisecond
	streamEvents     = 20
	streamPace       = 100 * time.Millisecond // 20 events x 100 ms = 2 s, 6.7x the ReadTimeout
)

// streamOutcome is what the handler side of a long stream saw.
type streamOutcome struct {
	written    int           // events written without error
	writeErr   error         // first write error, nil if none
	ctxErr     error         // request context error when the handler returned
	ctxEndedAt time.Duration // time since the handler started when the context ended, 0 = never
}

// streamServer listens on a real socket with an http.Server like echo.StartConfig builds
// (configure sets the server timeouts) and serves the API instance with a stream route on /raw (headers + flush only) and on
// /stream (httpx.OpenStream). Each handler reports its outcome on the returned channel.
func streamServer(t *testing.T, opts httpx.Options, configure func(*http.Server)) (base string, out chan streamOutcome) {
	t.Helper()
	e := httpx.New(opts)
	out = make(chan streamOutcome, 4)

	e.GET("/raw", func(c *echo.Context) error {
		h := c.Response().Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		c.Response().WriteHeader(http.StatusOK)
		flush := http.NewResponseController(c.Response()).Flush
		out <- pump(c.Request().Context(), func(i int) error {
			if _, err := io.WriteString(c.Response(), "id: "+strconv.Itoa(i)+"\nevent: info\ndata: {}\n\n"); err != nil {
				return err
			}
			return flush()
		})
		return nil
	})
	e.GET("/runs/:id/events", func(c *echo.Context) error {
		st, err := httpx.OpenStream(c)
		if err != nil {
			return err
		}
		out <- pump(c.Request().Context(), func(i int) error {
			return st.Event(httpx.StreamEvent{ID: strconv.Itoa(i), Name: "info", Data: "{}"})
		})
		return nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: e}
	configure(srv)
	var wg sync.WaitGroup
	wg.Go(func() { _ = srv.Serve(ln) })
	t.Cleanup(func() {
		_ = srv.Close()
		wg.Wait()
	})
	return "http://" + ln.Addr().String(), out
}

// pump writes streamEvents events, one per streamPace, like a run that publishes steadily.
// It stops at the first write error or when the request context ends, and reports both.
func pump(ctx context.Context, write func(i int) error) streamOutcome {
	var o streamOutcome
	start := time.Now()
	tick := time.NewTicker(streamPace)
	defer tick.Stop()
	for i := 1; i <= streamEvents; i++ {
		select {
		case <-ctx.Done():
			o.ctxErr, o.ctxEndedAt = ctx.Err(), time.Since(start)
			return o
		case <-tick.C:
		}
		if err := write(i); err != nil {
			o.writeErr = err
			break
		}
		o.written++
	}
	o.ctxErr = ctx.Err()
	return o
}

// getStream opens path and reads the body until the server ends it, returning the response
// head and the body read so far (also on a broken connection).
func getStream(t *testing.T, url string, hdr http.Header) (*http.Response, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", url, err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, rerr := io.ReadAll(resp.Body)
	return resp, string(body), rerr
}

func countFrames(body string) int { return strings.Count(body, "event: info\n") }

func readTimeout(srv *http.Server)  { srv.ReadTimeout = shortReadTimeout }
func writeTimeout(srv *http.Server) { srv.WriteTimeout = shortReadTimeout }

// TestReadTimeout_DoesNotCancelBodylessStream settles planner decision P2 (F1): Echo v5.4.0's
// hard-coded ReadTimeout of 30 s does NOT end a streaming GET. net/http clears the connection's
// read deadline when it starts the background read of a request without body
// (connReader.startBackgroundRead), so the request context lives on and every write succeeds,
// although the stream is 6.7x longer than the ReadTimeout. The same holds for a request whose
// body was sent but never read by the handler. Test and handler use plain headers + flush, no
// OpenStream. A failure here means a newer Go changed that and OpenStream becomes load-bearing.
func TestReadTimeout_DoesNotCancelBodylessStream(t *testing.T) {
	t.Parallel()
	base, out := streamServer(t, httpx.Options{}, readTimeout)

	_, body, err := getStream(t, base+"/raw", nil)
	got := <-out

	t.Logf("ReadTimeout %v, no OpenStream: written=%d seen by client=%d ctxErr=%v writeErr=%v readErr=%v",
		shortReadTimeout, got.written, countFrames(body), got.ctxErr, got.writeErr, err)
	if err != nil || got.ctxErr != nil || got.writeErr != nil || got.written != streamEvents {
		t.Errorf("stream under ReadTimeout = {written %d, ctxErr %v, writeErr %v, readErr %v}, want a full uncancelled stream of %d events",
			got.written, got.ctxErr, got.writeErr, err, streamEvents)
	}
}

// TestReadTimeout_DoesNotCancelStreamOfRequestWithBody covers the other branch: a GET that
// carries a body the handler never reads has no background read and keeps its read deadline,
// but a deadline on reads does not touch the context or the writes either.
func TestReadTimeout_DoesNotCancelStreamOfRequestWithBody(t *testing.T) {
	t.Parallel()
	base, out := streamServer(t, httpx.Options{}, readTimeout)

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := io.WriteString(conn, "GET /raw HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	body, err := io.ReadAll(conn)
	got := <-out

	t.Logf("ReadTimeout %v, request with body, no OpenStream: written=%d seen by client=%d ctxErr=%v writeErr=%v readErr=%v",
		shortReadTimeout, got.written, countFrames(string(body)), got.ctxErr, got.writeErr, err)
	if err != nil || got.ctxErr != nil || got.writeErr != nil || got.written != streamEvents {
		t.Errorf("stream of a request with body under ReadTimeout = {written %d, ctxErr %v, writeErr %v, readErr %v}, want a full stream of %d events",
			got.written, got.ctxErr, got.writeErr, err, streamEvents)
	}
}

// TestWriteTimeout_CutsPlainStreamButNotOpenStream shows what OpenStream's deadline handling
// is for: a WriteTimeout on the server (which Echo does not set, but a BeforeServeFunc could)
// cuts a plain streaming response once it expires, while OpenStream lifts it and arms a
// deadline per frame instead.
func TestWriteTimeout_CutsPlainStreamButNotOpenStream(t *testing.T) {
	t.Parallel()
	base, out := streamServer(t, httpx.Options{}, writeTimeout)

	_, rawBody, _ := getStream(t, base+"/raw", nil)
	raw := <-out
	t.Logf("WriteTimeout %v, no OpenStream: written=%d seen by client=%d ctxErr=%v after %v writeErr=%v",
		shortReadTimeout, raw.written, countFrames(rawBody), raw.ctxErr, raw.ctxEndedAt, raw.writeErr)
	if raw.written >= streamEvents || (raw.writeErr == nil && raw.ctxErr == nil) {
		t.Errorf("plain stream under WriteTimeout = {written %d, ctxErr %v, writeErr %v}, want it cut (write error or canceled context) before %d events",
			raw.written, raw.ctxErr, raw.writeErr, streamEvents)
	}

	_, body, err := getStream(t, base+"/runs/r1/events", nil)
	got := <-out
	t.Logf("WriteTimeout %v, OpenStream: written=%d seen by client=%d writeErr=%v", shortReadTimeout, got.written, countFrames(body), got.writeErr)
	if err != nil || got.writeErr != nil || got.ctxErr != nil || got.written != streamEvents || countFrames(body) != streamEvents {
		t.Errorf("OpenStream under WriteTimeout = {written %d, ctxErr %v, writeErr %v, readErr %v, client frames %d}, want a full stream of %d events",
			got.written, got.ctxErr, got.writeErr, err, countFrames(body), streamEvents)
	}
}

// TestOpenStream_SurvivesReadTimeout: with OpenStream the same stream under ReadTimeout also
// delivers every event.
func TestOpenStream_SurvivesReadTimeout(t *testing.T) {
	t.Parallel()
	base, out := streamServer(t, httpx.Options{}, readTimeout)

	_, body, err := getStream(t, base+"/runs/r1/events", nil)
	got := <-out

	if err != nil {
		t.Errorf("reading the stream: %v", err)
	}
	if got.ctxErr != nil || got.writeErr != nil || got.written != streamEvents {
		t.Errorf("stream outcome = {written %d, ctxErr %v, writeErr %v}, want {written %d, ctxErr nil, writeErr nil}",
			got.written, got.ctxErr, got.writeErr, streamEvents)
	}
	if n := countFrames(body); n != streamEvents {
		t.Errorf("events seen by the client = %d, want %d", n, streamEvents)
	}
}

// TestOpenStream_FramesAndHeaders pins the fastify-sse-v2@4.2.2 framing and headers (spec 7.4).
func TestOpenStream_FramesAndHeaders(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	e := httpx.New(httpx.Options{})
	e.GET("/s", func(c *echo.Context) error {
		st, err := httpx.OpenStream(c)
		if err != nil {
			return err
		}
		for _, ev := range []httpx.StreamEvent{
			{ID: "1", Name: "info", Data: `{"seq":1}`},
			{ID: "2", Name: "result", Data: "a\nb"},
			{Data: "only data"},
			{},
		} {
			if err := st.Event(ev); err != nil {
				return err
			}
		}
		return nil
	})
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s", nil))

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	wantHeaders := map[string]string{
		"Content-Type":     "text/event-stream; charset=utf-8",
		"Cache-Control":    "no-cache,no-transform",
		"Connection":       "keep-alive",
		"X-No-Compression": "1",
		"X-Frame-Options":  "SAMEORIGIN", // Phase 1 security headers are kept
	}
	for k, want := range wantHeaders {
		if got := res.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if got := res.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	wantBody := "retry: 3000\n\n" +
		"id: 1\nevent: info\ndata: {\"seq\":1}\n\n" +
		"id: 2\nevent: result\ndata: a\ndata: b\n\n" +
		"data: only data\n\n"
	if diff := cmp.Diff(wantBody, rec.Body.String()); diff != "" {
		t.Errorf("stream body mismatch (-want +got):\n%s", diff)
	}
}

func TestOpenStream_RejectsMultiLineIDAndName(t *testing.T) {
	t.Parallel()
	e := httpx.New(httpx.Options{})
	var errs [2]error
	e.GET("/s", func(c *echo.Context) error {
		st, err := httpx.OpenStream(c)
		if err != nil {
			return err
		}
		errs[0] = st.Event(httpx.StreamEvent{ID: "1\ndata: forged"})
		errs[1] = st.Event(httpx.StreamEvent{Name: "x\r\n\r\n"})
		return nil
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s", nil))

	for i, err := range errs {
		if err == nil {
			t.Errorf("Event with a line break in field %d returned nil, want an error", i)
		}
	}
	if got, want := rec.Body.String(), "retry: 3000\n\n"; got != want {
		t.Errorf("body after rejected events = %q, want %q", got, want)
	}
}

func TestOpenStream_RefusesCommittedResponse(t *testing.T) {
	t.Parallel()
	e := httpx.New(httpx.Options{})
	var openErr error
	e.GET("/s", func(c *echo.Context) error {
		if err := c.NoContent(http.StatusNoContent); err != nil {
			return err
		}
		_, openErr = httpx.OpenStream(c)
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s", nil))
	if openErr == nil {
		t.Error("OpenStream after the response was committed returned nil, want an error")
	}
}

// TestStreamRoute_SecurityOverRealListener runs the stream route through the whole API stack:
// Host 403 before any streaming, GET without an Origin check, CORS headers for an allowed
// origin only, and exactly one request-log line (status 200) once the stream has ended.
func TestStreamRoute_SecurityOverRealListener(t *testing.T) {
	t.Parallel()
	var logBuf syncBuffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	base, out := streamServer(t, httpx.Options{
		Log:          log,
		CORSOrigins:  []string{webOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	}, readTimeout)

	t.Run("foreign Host is refused before streaming", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/runs/r1/events", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Host = "evil.example"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET with foreign Host: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("Content-Type = %q, want the JSON error envelope", ct)
		}
		if !bytes.Contains(body, []byte(`"error"`)) {
			t.Errorf("body = %q, want the error envelope", body)
		}
	})

	tests := []struct {
		name       string
		origin     string
		wantCORS   string
		wantStatus int
	}{
		{name: "allowed origin gets CORS headers", origin: webOrigin, wantCORS: webOrigin, wantStatus: 200},
		{name: "foreign origin on GET streams without CORS headers", origin: "http://evil.example", wantCORS: "", wantStatus: 200},
		{name: "no origin streams", origin: "", wantCORS: "", wantStatus: 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := http.Header{}
			if tc.origin != "" {
				hdr.Set("Origin", tc.origin)
			}
			resp, body, err := getStream(t, base+"/runs/r1/events", hdr)
			got := <-out
			if err != nil {
				t.Errorf("reading the stream: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if g := resp.Header.Get("Access-Control-Allow-Origin"); g != tc.wantCORS {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", g, tc.wantCORS)
			}
			if tc.wantCORS != "" && resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
				t.Error("Access-Control-Allow-Credentials missing on the allowed origin")
			}
			if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("security headers missing on the stream")
			}
			if !strings.HasPrefix(body, "retry: 3000\n\n") || countFrames(body) != streamEvents || got.written != streamEvents {
				t.Errorf("stream: first frame ok=%v, frames=%d (written %d), want retry first and %d frames",
					strings.HasPrefix(body, "retry: 3000\n\n"), countFrames(body), got.written, streamEvents)
			}
		})
	}

	// The log line is written after the handler returns, which can be just after the client saw EOF.
	deadline := time.Now().Add(2 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		lines = requestLines(logBuf.String(), "/runs/r1/events")
		if len(lines) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Four requests hit the route: the 403 and three streams; each logs exactly once.
	var s200, s403 int
	for _, l := range lines {
		switch {
		case strings.Contains(l, "status=200"):
			s200++
		case strings.Contains(l, "status=403"):
			s403++
		}
	}
	if len(lines) != 4 || s200 != 3 || s403 != 1 {
		t.Errorf("request log lines for the stream route = %d (200: %d, 403: %d), want 4 (3, 1):\n%s",
			len(lines), s200, s403, strings.Join(lines, "\n"))
	}
}

// TestOpenStream_ClientDisconnectEndsWrites: a client that goes away makes the write fail or
// cancels the request context soon, so the handler can release its subscription (EC4027).
func TestOpenStream_ClientDisconnectEndsWrites(t *testing.T) {
	t.Parallel()
	base, out := streamServer(t, httpx.Options{}, readTimeout)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/runs/r1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	r := bufio.NewReader(resp.Body)
	if line, err := r.ReadString('\n'); err != nil || line != "retry: 3000\n" {
		t.Fatalf("first line = %q, %v, want %q", line, err, "retry: 3000\n")
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case got := <-out:
		if got.written >= streamEvents {
			t.Errorf("events written after the client left = %d, want fewer than %d", got.written, streamEvents)
		}
		if got.ctxErr == nil && got.writeErr == nil {
			t.Error("handler saw neither a canceled context nor a write error after the client left")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler still running 3 s after the client disconnected")
	}
}

// requestLines returns the request log lines of the given path.
func requestLines(logs, path string) []string {
	var lines []string
	for l := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(l, "msg=request") && strings.Contains(l, "path="+path) {
			lines = append(lines, l)
		}
	}
	return lines
}

// syncBuffer is a bytes.Buffer safe for the logger (server goroutines) and the test goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
