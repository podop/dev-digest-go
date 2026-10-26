package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/logging"
)

// CustomClient returns a copy of hc for a custom provider (spec FR8, NFR1, NFR2): it never follows
// a redirect, and a transport error or a non-2xx response body reaches the caller with every
// secret replaced by [REDACTED] — before Do cuts the provider's message, so a secret cannot
// survive half cut. A custom gateway is operator-defined, so its error text is untrusted.
//
// hc must not be nil: the caller owns the outbound policy (the egress client with the address
// guard); this package never builds a client. Transport errors keep their chain (errors.Is/As
// still see the cause), only their text is scrubbed.
func CustomClient(hc *http.Client, secrets []string) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c.Transport = &scrubTransport{base: base, secrets: nonEmpty(secrets)}
	return &c
}

// ScrubText replaces every secret, and anything that looks like a (masked) provider key, in s.
func ScrubText(s string, secrets []string) string { return scrubAll(s, nonEmpty(secrets)) }

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

var (
	// keyLikeRun matches provider key shapes (sk-…, sk-or-…, sk-ant-…), masked or not.
	keyLikeRun = regexp.MustCompile(`(?i)\bsk-[\w*•…-]*`)
	// maskedRun matches any word run holding a mask (two or more of * • …): providers echo a
	// masked copy of a rejected key that an exact-match scrub misses.
	maskedRun = regexp.MustCompile(`[\w-]*[*•…]{2,}[\w-]*`)
)

func scrubAll(s string, secrets []string) string {
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, logging.Redacted)
		// The same secret inside a JSON string (a gateway echoing it in its error body).
		if q, err := json.Marshal(sec); err == nil {
			if esc := string(q[1 : len(q)-1]); esc != sec {
				s = strings.ReplaceAll(s, esc, logging.Redacted)
			}
		}
	}
	s = keyLikeRun.ReplaceAllString(s, logging.Redacted)
	return maskedRun.ReplaceAllString(s, logging.Redacted)
}

type scrubTransport struct {
	base    http.RoundTripper
	secrets []string
}

func (t *scrubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if msg := scrubAll(err.Error(), t.secrets); msg != err.Error() {
			return nil, &scrubbedError{msg: msg, err: err}
		}
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	_ = resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	clean := []byte(scrubAll(string(raw), t.secrets))
	resp.Body = io.NopCloser(bytes.NewReader(clean))
	resp.ContentLength = int64(len(clean))
	resp.Header.Set("Content-Length", strconv.Itoa(len(clean)))
	return resp, nil
}

// scrubbedError carries the scrubbed text of err and keeps err reachable for errors.Is/As.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// CustomHeaders builds the request headers of a custom provider (spec FR4): the defaults, then
// the extra headers (so an extra header named like a default wins), then the auth header, which
// nothing can replace. Names are canonical, so one name is one entry. An empty mode is bearer, an
// empty authHeader in header mode is custom.DefaultAuthHeader, and an empty key sends no auth
// header. The error never contains a header value.
func CustomHeaders(mode custom.AuthMode, authHeader, key string, extra, defaults map[string]string) (map[string]string, error) {
	if mode == "" {
		mode = custom.AuthBearer
	}
	if !mode.Valid() {
		return nil, errors.New("invalid auth mode")
	}
	if mode == custom.AuthHeader {
		if authHeader == "" {
			authHeader = custom.DefaultAuthHeader
		}
		if len(authHeader) > custom.MaxHeaderNameLen || !custom.IsToken(authHeader) {
			return nil, errors.New("invalid auth header")
		}
	} else {
		authHeader = ""
	}
	if len(extra) > custom.MaxHeaders {
		return nil, fmt.Errorf("at most %d extra headers are allowed", custom.MaxHeaders)
	}
	out := make(map[string]string, len(defaults)+len(extra)+1)
	for name, value := range defaults {
		out[http.CanonicalHeaderKey(name)] = value
	}
	seen := make(map[string]struct{}, len(extra))
	for name, value := range extra {
		if msg := custom.CheckHeaderName(name, authHeader); msg != "" {
			return nil, fmt.Errorf("extra header %q: %s", name, msg)
		}
		if msg := custom.CheckHeaderValue(value); msg != "" {
			return nil, fmt.Errorf("extra header %q: %s", name, msg)
		}
		canon := http.CanonicalHeaderKey(name)
		if _, dup := seen[canon]; dup {
			return nil, fmt.Errorf("extra header %q is listed twice", name)
		}
		seen[canon] = struct{}{}
		out[canon] = value
	}
	if key != "" {
		if mode == custom.AuthBearer {
			out["Authorization"] = "Bearer " + key
		} else {
			out[http.CanonicalHeaderKey(authHeader)] = key
		}
	}
	return out, nil
}

// JSONNumber reads raw as a JSON number; anything else (string, null, object) and any number that
// does not fit a finite float64 is not a number.
func JSONNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
