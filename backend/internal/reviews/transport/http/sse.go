package reviewshttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/reviews/domain"
)

// events answers GET /runs/:id/events (spec 7.4). Everything that can fail with a JSON error
// (422, 404) happens before the stream is opened; afterwards the handler only returns nil: a
// failed write means the client is gone, the request context ends when it disconnects, and the
// subscription is always released. A live stream carries no status event and simply closes when
// the run completes.
func (h *handler) events(c *echo.Context) error {
	id, ws, err := h.scope(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	sub, err := h.api.Events(ctx, ws, id)
	if err != nil {
		return err
	}
	defer sub.Close()

	if c.Request().Method == http.MethodHead {
		// A HEAD request is answered by the GET handler (AutoHandleHEAD); it gets the headers only.
		hdr := c.Response().Header()
		hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
		hdr.Set("Connection", "keep-alive")
		hdr.Set("Cache-Control", "no-cache,no-transform")
		hdr.Set("x-no-compression", "1")
		return c.NoContent(http.StatusOK)
	}

	stream, err := httpx.OpenStream(c)
	if err != nil {
		if resp, uerr := echo.UnwrapResponse(c.Response()); uerr == nil && resp.Committed {
			return nil // the first frame failed to reach the client
		}
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-sub.C():
			if !ok {
				return nil
			}
			data, err := eventData(ev)
			if err != nil {
				return nil
			}
			if err := stream.Event(httpx.StreamEvent{ID: strconv.Itoa(ev.Seq), Name: string(ev.Kind), Data: data}); err != nil {
				return nil
			}
		}
	}
}

// eventWire is the data of one frame: keys in contract order, data only when the event has any.
type eventWire struct {
	RunID string          `json:"runId"`
	Seq   int             `json:"seq"`
	Kind  string          `json:"kind"`
	Msg   string          `json:"msg"`
	T     string          `json:"t"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// eventData renders an event as the single line JSON.stringify would produce: no HTML escaping
// (Go's encoder escapes <, > and & by default), no trailing newline, and U+2028/U+2029 left as
// they are (Go's encoder always escapes them; JSON.stringify never does).
func eventData(ev domain.RunEvent) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(eventWire{
		RunID: ev.RunID, Seq: ev.Seq, Kind: string(ev.Kind), Msg: ev.Msg, T: ev.T, Data: ev.Data,
	})
	if err != nil {
		return "", err
	}
	return string(unescapeLineSeparators(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))), nil
}

// unescapeLineSeparators replaces the JSON escapes   and   by the characters
// themselves. Escapes are read pairwise from each backslash, so an escaped backslash followed by
// the text u2028 is left alone.
func unescapeLineSeparators(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			continue
		}
		if b[i+1] == 'u' && i+5 < len(b) && string(b[i+2:i+5]) == "202" {
			switch b[i+5] {
			case '8':
				out = append(out, " "...)
				i += 5
				continue
			case '9':
				out = append(out, " "...)
				i += 5
				continue
			}
		}
		out = append(out, b[i], b[i+1])
		i++
	}
	return out
}
