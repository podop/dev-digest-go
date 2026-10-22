package reviewshttp_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
	reviewshttp "dev-digest-go/backend/internal/reviews/transport/http"
)

// goldenPath is the Node SSE recording of the parity step (fastify-sse-v2 4.2.2 over a socket).
const goldenPath = "../../parity/testdata/goldens/sse.json"

type goldenResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// blob is the golden's stand-in for a body too large to inline.
type blob struct {
	Len    int    `json:"len"`
	SHA256 string `json:"sha256"`
	Head   string `json:"head"`
	Tail   string `json:"tail"`
}

// checkBody compares the body with the recording: an inline string is normalised and diffed, a
// $blob is checked by length, digest, head and tail.
func (g goldenResponse) checkBody(t *testing.T, got string) {
	t.Helper()
	var s string
	if err := json.Unmarshal(g.Body, &s); err == nil {
		if diff := cmp.Diff(normalise(s), normalise(got)); diff != "" {
			t.Errorf("body mismatch (-node +go):\n%s", diff)
		}
		return
	}
	var b blob
	if err := json.Unmarshal(g.Body, &b); err != nil || b.Len == 0 {
		t.Fatalf("golden body is neither a string nor a blob: %s", g.Body)
	}
	sum := sha256.Sum256([]byte(normalise(got)))
	if len(got) != b.Len || !strings.HasPrefix(normalise(got), normalise(b.Head)) || !strings.HasSuffix(normalise(got), normalise(b.Tail)) {
		t.Errorf("blob body: len %d head/tail ok=%v, want len %d", len(got), strings.HasPrefix(got, b.Head), b.Len)
	}
	if hex.EncodeToString(sum[:]) != b.SHA256 {
		t.Logf("blob digest differs (the recording digests the raw clock stamps, so only length, head and tail are asserted)")
	}
}

type goldenFile struct {
	Synthetic []struct {
		Name   string `json:"name"`
		RunID  string `json:"runId"`
		Events []struct {
			Kind string          `json:"kind"`
			Msg  json.RawMessage `json:"msg"`
			Data json.RawMessage `json:"data"`
		} `json:"events"`
		Response goldenResponse `json:"response"`
	} `json:"synthetic"`
	FinalStatus []struct {
		Name     string         `json:"name"`
		RunID    string         `json:"runId"`
		DBStatus *string        `json:"dbStatus"`
		DBError  *string        `json:"dbError"`
		Response goldenResponse `json:"response"`
	} `json:"finalStatus"`
	Errors []struct {
		Name     string `json:"name"`
		Request  struct{ Path string }
		Response goldenResponse `json:"response"`
	} `json:"errors"`
}

// goldenMsg reads a message that is a string or {"$repeat":[text, count]}.
func goldenMsg(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var rep struct {
		Repeat []json.RawMessage `json:"$repeat"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil || len(rep.Repeat) != 2 {
		t.Fatalf("unsupported golden message %s", raw)
	}
	var unit string
	var n int
	if json.Unmarshal(rep.Repeat[0], &unit) != nil || json.Unmarshal(rep.Repeat[1], &n) != nil {
		t.Fatalf("unsupported golden $repeat %s", raw)
	}
	return strings.Repeat(unit, n)
}

var clockStamp = regexp.MustCompile(`"t":"\d\d:\d\d:\d\d"`)

// normalise blanks the clock stamps, which come from the Node recording's logical clock.
func normalise(s string) string { return clockStamp.ReplaceAllString(s, `"t":"T"`) }

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse %s: %v", goldenPath, err)
	}
	return g
}

// closedSub is a subscription holding the events, already completed.
func closedSub(events ...domain.RunEvent) *chanSub {
	s := newChanSub(events...)
	close(s.ch)
	return s
}

func streamServerFor(t *testing.T, sub *chanSub) (*fakeAPI, response) {
	t.Helper()
	api := &fakeAPI{events: func() (app.Subscription, error) { return sub, nil }}
	srv := newServer(t, api)
	return api, send(t, srv, "GET", "/runs/"+otherID+"/events", "")
}

func checkStreamHeaders(t *testing.T, r response, want map[string]string) {
	t.Helper()
	for _, k := range []string{"content-type", "connection", "cache-control", "x-no-compression"} {
		if got := r.Header.Get(k); got != want[k] {
			t.Errorf("header %s = %q, want %q", k, got, want[k])
		}
	}
}

func TestEvents_MatchesTheNodeRecordingForPublishedEvents(t *testing.T) {
	g := loadGolden(t)
	if len(g.Synthetic) == 0 {
		t.Fatal("golden has no synthetic streams")
	}
	for _, tc := range g.Synthetic {
		t.Run(tc.Name, func(t *testing.T) {
			var events []domain.RunEvent
			for i, e := range tc.Events {
				var data json.RawMessage
				if len(e.Data) > 0 && string(e.Data) != "null" {
					data = e.Data
				}
				events = append(events, domain.RunEvent{
					RunID: tc.RunID, Seq: i + 1, Kind: reviewer.EventKind(e.Kind), Msg: goldenMsg(t, e.Msg), T: "12:00:00", Data: data,
				})
			}
			sub := closedSub(events...)
			_, r := streamServerFor(t, sub)
			if r.Status != tc.Response.Status {
				t.Fatalf("status = %d, want %d", r.Status, tc.Response.Status)
			}
			checkStreamHeaders(t, r, tc.Response.Headers)
			tc.Response.checkBody(t, r.Body)
			waitClosed(t, sub)
		})
	}
}

func TestEvents_MatchesTheNodeRecordingForFinishedRuns(t *testing.T) {
	g := loadGolden(t)
	now := time.Date(2026, 10, 22, 12, 0, 12, 0, time.UTC)
	for _, tc := range g.FinalStatus {
		t.Run(tc.Name, func(t *testing.T) {
			status := domain.RunStatus("")
			if tc.DBStatus != nil {
				status = domain.RunStatus(*tc.DBStatus)
			}
			sub := closedSub(domain.FinalStatusEvent(tc.RunID, status, tc.DBError, now))
			_, r := streamServerFor(t, sub)
			tc.Response.checkBody(t, r.Body)
		})
	}
}

func TestEvents_ErrorsBeforeStreamingAreJSON(t *testing.T) {
	g := loadGolden(t)
	for _, tc := range g.Errors {
		t.Run(tc.Name, func(t *testing.T) {
			api := &fakeAPI{events: func() (app.Subscription, error) { return nil, apperr.NotFound("Run not found") }}
			srv := newServer(t, api)
			r := send(t, srv, "GET", tc.Request.Path, "")
			if r.Status != tc.Response.Status {
				t.Fatalf("status = %d (%s), want %d", r.Status, r.Body, tc.Response.Status)
			}
			// The error envelope is rendered by httpx (Phase 1): compare media type, code and message.
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			var want, got struct {
				Error struct{ Code, Message string }
			}
			var wantBody string
			if err := json.Unmarshal(tc.Response.Body, &wantBody); err != nil {
				t.Fatalf("golden body: %v", err)
			}
			if err := json.Unmarshal([]byte(wantBody), &want); err != nil {
				t.Fatalf("golden envelope: %v", err)
			}
			if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
				t.Fatalf("envelope %q: %v", r.Body, err)
			}
			if tc.Response.Status == http.StatusNotFound && got != want {
				t.Errorf("error = %+v, want %+v", got.Error, want.Error)
			}
			if tc.Response.Status == http.StatusUnprocessableEntity && got.Error.Code != want.Error.Code {
				t.Errorf("error code = %q, want %q", got.Error.Code, want.Error.Code)
			}
		})
	}
}

func TestEvents_LineSeparatorsAndMarkupAreNotEscaped(t *testing.T) {
	msg := "a\u2028b\u2029c <x> & \\u2028 \"q\" \n\t\u0001"
	sub := closedSub(domain.RunEvent{RunID: otherID, Seq: 1, Kind: reviewer.EventInfo, Msg: msg, T: "12:00:00"})
	_, r := streamServerFor(t, sub)
	// JSON.stringify keeps U+2028/U+2029 and <>& as they are and escapes the control characters.
	want := "retry: 3000\n\nid: 1\nevent: info\ndata: {\"runId\":\"" + otherID + "\",\"seq\":1,\"kind\":\"info\",\"msg\":\"" +
		"a\u2028b\u2029c <x> & \\\\u2028 \\\"q\\\" \\n\\t\\u0001\",\"t\":\"12:00:00\"}\n\n"
	if diff := cmp.Diff(want, r.Body); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestEvents_DataIsEmittedAsGivenAndOnlyWhenSet(t *testing.T) {
	sub := closedSub(
		domain.RunEvent{RunID: otherID, Seq: 1, Kind: reviewer.EventTool, Msg: "m", T: "00:00:01", Data: json.RawMessage(`{"b":1,"a":"<x>"}`)},
		domain.RunEvent{RunID: otherID, Seq: 2, Kind: reviewer.EventError, Msg: "e", T: "00:00:02"},
	)
	_, r := streamServerFor(t, sub)
	want := "retry: 3000\n\n" +
		"id: 1\nevent: tool\ndata: {\"runId\":\"" + otherID + "\",\"seq\":1,\"kind\":\"tool\",\"msg\":\"m\",\"t\":\"00:00:01\",\"data\":{\"b\":1,\"a\":\"<x>\"}}\n\n" +
		"id: 2\nevent: error\ndata: {\"runId\":\"" + otherID + "\",\"seq\":2,\"kind\":\"error\",\"msg\":\"e\",\"t\":\"00:00:02\"}\n\n"
	if diff := cmp.Diff(want, r.Body); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestEvents_LiveStreamFlushesEachEventAndEndsOnComplete(t *testing.T) {
	sub := newChanSub()
	api := &fakeAPI{events: func() (app.Subscription, error) { return sub, nil }}
	srv := newServer(t, api)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/runs/"+otherID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	readFrame := func() string {
		t.Helper()
		var sb strings.Builder
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("read frame: %v (got %q)", err, sb.String())
			}
			sb.WriteString(line)
			if line == "\n" {
				return sb.String()
			}
		}
	}
	if got := readFrame(); got != "retry: 3000\n\n" {
		t.Errorf("first frame = %q, want the retry frame", got)
	}
	// Events published while the client waits arrive one by one, before the stream ends.
	for i := 1; i <= 3; i++ {
		sub.ch <- domain.RunEvent{RunID: otherID, Seq: i, Kind: reviewer.EventInfo, Msg: "m", T: "00:00:00"}
		got := readFrame()
		if !strings.HasPrefix(got, "id: "+string(rune('0'+i))+"\nevent: info\ndata: {") {
			t.Errorf("frame %d = %q", i, got)
		}
	}
	close(sub.ch) // the run completed
	rest := make([]byte, 1)
	if n, err := rd.Read(rest); n != 0 || err == nil {
		t.Errorf("after complete: read %d bytes, err %v; want the response to end", n, err)
	}
	waitClosed(t, sub)
}

func TestEvents_ClientDisconnectReleasesTheSubscription(t *testing.T) {
	sub := newChanSub()
	api := &fakeAPI{events: func() (app.Subscription, error) { return sub, nil }}
	srv := newServer(t, api)

	ctx, cancel := contextWithCancel(t)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/runs/"+otherID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	rd := bufio.NewReader(resp.Body)
	if line, err := rd.ReadString('\n'); err != nil || line != "retry: 3000\n" {
		t.Fatalf("first line = %q, %v", line, err)
	}
	cancel()
	_ = resp.Body.Close()
	waitClosed(t, sub)
	if n := sub.closeCount(); n != 1 {
		t.Errorf("Close calls = %d, want 1", n)
	}
}

func TestEvents_HeadAnswersHeadersAndReleasesTheSubscription(t *testing.T) {
	sub := newChanSub()
	api := &fakeAPI{events: func() (app.Subscription, error) { return sub, nil }}
	srv := newServer(t, api)
	r := send(t, srv, "HEAD", "/runs/"+otherID+"/events", "")
	if r.Status != 200 || r.Body != "" {
		t.Errorf("HEAD = %d %q, want 200 and no body", r.Status, r.Body)
	}
	if got := r.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	waitClosed(t, sub)
}

func waitClosed(t *testing.T, s *chanSub) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("subscription was not closed")
	}
}

// streamFarm opens event streams against a server whose fake API hands out a fresh chanSub per
// stream and remembers them in order.
type streamFarm struct {
	srv  *httptest.Server
	mu   sync.Mutex
	subs []*chanSub
}

func newStreamFarm(t *testing.T) *streamFarm {
	t.Helper()
	f := &streamFarm{}
	api := &fakeAPI{events: func() (app.Subscription, error) {
		s := newChanSub()
		f.mu.Lock()
		f.subs = append(f.subs, s)
		f.mu.Unlock()
		return s, nil
	}}
	f.srv = newServer(t, api)
	return f
}

// open starts a stream of the run and returns the status, the body when it was refused, and a
// function that disconnects the client. An accepted stream is open once its first frame arrived.
func (f *streamFarm) open(t *testing.T, run string) (status int, body string, disconnect func()) {
	t.Helper()
	ctx, cancel := contextWithCancel(t)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/runs/"+run+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events of %s: %v", run, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), cancel
	}
	return resp.StatusCode, "", func() { cancel(); _ = resp.Body.Close() }
}

func (f *streamFarm) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

func (f *streamFarm) sub(i int) *chanSub {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs[i]
}

func wantRateLimited(t *testing.T, what string, status int, body string) {
	t.Helper()
	if status != http.StatusTooManyRequests {
		t.Fatalf("%s: status = %d, want 429 (body %q)", what, status, body)
	}
	var got struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Error.Code != "rate_limited" || got.Error.Message == "" {
		t.Errorf("%s: body = %q (%v), want the error envelope with code rate_limited", what, body, err)
	}
}

func TestEvents_OpenStreamsPerRunAreCapped(t *testing.T) {
	f := newStreamFarm(t)
	const runA, runB = otherID, "7e1d0000-0000-4000-8000-000000000002"
	var first func()
	for i := range reviewshttp.MaxStreamsPerRun {
		status, _, disconnect := f.open(t, runA)
		if status != http.StatusOK {
			t.Fatalf("stream %d of the run: status = %d, want 200", i+1, status)
		}
		if i == 0 {
			first = disconnect
		}
	}
	status, body, _ := f.open(t, runA)
	wantRateLimited(t, "stream over the per-run cap", status, body)
	if n := f.count(); n != reviewshttp.MaxStreamsPerRun+1 {
		t.Fatalf("subscriptions opened = %d, want %d", n, reviewshttp.MaxStreamsPerRun+1)
	}
	waitClosed(t, f.sub(reviewshttp.MaxStreamsPerRun)) // the refused request released its subscription

	if status, _, _ := f.open(t, runB); status != http.StatusOK {
		t.Errorf("stream of another run: status = %d, want 200 (the cap is per run)", status)
	}

	// Closing one stream frees its slot.
	first()
	waitClosed(t, f.sub(0))
	if status, body, _ := f.open(t, runA); status != http.StatusOK {
		t.Errorf("stream after one was closed: status = %d (%s), want 200", status, body)
	}
}

func TestEvents_OpenStreamsOverallAreCapped(t *testing.T) {
	f := newStreamFarm(t)
	runID := func(i int) string { return fmt.Sprintf("7e1d0000-0000-4000-8000-%012d", i) }
	for i := range reviewshttp.MaxStreams {
		// Runs are filled to the per-run cap one after the other.
		run := runID(i / reviewshttp.MaxStreamsPerRun)
		if status, body, _ := f.open(t, run); status != http.StatusOK {
			t.Fatalf("stream %d: status = %d (%s), want 200", i+1, status, body)
		}
	}
	status, body, _ := f.open(t, runID(1_000))
	wantRateLimited(t, "stream over the overall cap", status, body)
}
