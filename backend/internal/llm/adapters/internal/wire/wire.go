// Package wire is the JSON-over-HTTP plumbing shared by the provider adapters: one request, a
// capped response body, provider errors carrying their status. It sets no timeout of its own;
// the caller's context decides.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/logging"
)

// MessageKeyRejected is the fixed text of a 401 or 403 from a custom gateway that was sent a key.
const MessageKeyRejected = "API key rejected by the provider"

const (
	maxBodyBytes    = 16 << 20
	maxMessageBytes = 300
)

// StatusError is a non-2xx provider response. retry.IsTransient reads StatusCode.
type StatusError struct {
	Provider string
	Op       string
	Status   int
	Message  string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s returned %d", e.Provider, e.Op, e.Status)
	}
	return fmt.Sprintf("%s %s returned %d: %s", e.Provider, e.Op, e.Status, e.Message)
}

// StatusCode is the HTTP status.
func (e *StatusError) StatusCode() int { return e.Status }

// Call describes one request.
type Call struct {
	Provider string // display name, for errors
	Op       string // "chat completions", "/models", ... for errors
	Method   string
	URL      string
	Headers  map[string]string
	// Body, when non-nil, is sent as JSON.
	Body any
	// Key is scrubbed from error texts.
	Key string
	// ErrorMessage includes the provider's own error message in a StatusError.
	ErrorMessage bool
	// ResponseHeader, when non-nil, receives the headers of a 2xx response (a LiteLLM gateway
	// reports the call cost there, see GatewayCost).
	ResponseHeader *http.Header
	// Gateway marks a call to a custom gateway, whose error text is untrusted (spec NFR1): the
	// *url.Error of a transport failure is unwrapped (its text embeds the request URL), a 401 or
	// 403 carries a fixed message and any other provider message is scrubbed before it is cut.
	Gateway *Gateway
}

// Gateway is what Do needs to keep a custom gateway's error text clean.
type Gateway struct {
	// Secrets are the key and the extra header values of the provider.
	Secrets []string
	// Keyed says a key is configured; it only picks the wording of the fixed 401/403 message.
	Keyed bool
}

// Do performs the call and decodes a 2xx JSON response into out. The key never appears in the
// returned error.
func Do(ctx context.Context, hc *http.Client, c Call, out any) error {
	var body io.Reader
	if c.Body != nil {
		b, err := json.Marshal(c.Body)
		if err != nil {
			return fmt.Errorf("%s %s: encode request: %w", c.Provider, c.Op, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, c.URL, body)
	if err != nil {
		return fmt.Errorf("%s %s: %s", c.Provider, c.Op, scrub(err.Error(), c.Key))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dev-digest-go")
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if c.Gateway != nil {
			err = egress.Unwrap(err)
		}
		// The transport error carries the URL, never the headers.
		return fmt.Errorf("%s %s: %w", c.Provider, c.Op, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", c.Provider, c.Op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &StatusError{Provider: c.Provider, Op: c.Op, Status: resp.StatusCode}
		switch {
		case c.Gateway != nil:
			se.Message = c.Gateway.message(resp.StatusCode, raw, c.ErrorMessage)
		case c.ErrorMessage:
			se.Message = apiMessage(c.Key, raw)
		}
		return se
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New(c.Provider + " " + c.Op + " returned an unreadable response")
	}
	if c.ResponseHeader != nil {
		*c.ResponseHeader = resp.Header
	}
	return nil
}

// message is the text of a gateway's non-2xx answer: fixed for 401 and 403 (gateways echo a masked
// copy of the key there, which no scrub catches reliably), else the provider's message scrubbed
// of every secret and fragment of one before it is cut.
func (g *Gateway) message(status int, body []byte, withMessage bool) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if g.Keyed {
			return MessageKeyRejected
		}
		return fmt.Sprintf("The gateway rejected the request without a key (HTTP %d)", status)
	}
	if !withMessage {
		return ""
	}
	return cutMessage(custom.Scrub(errorMessage(body), g.Secrets, logging.Redacted))
}

func scrub(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, logging.Redacted)
}

// apiMessage extracts {"error":{"message":…}}, scrubbed of the key before it is cut.
func apiMessage(key string, body []byte) string {
	return cutMessage(scrub(errorMessage(body), key))
}

// errorMessage is the trimmed {"error":{"message":…}} of a body, "" when there is none.
func errorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Error.Message)
}

func cutMessage(msg string) string {
	if len(msg) > maxMessageBytes {
		msg = strings.ToValidUTF8(msg[:maxMessageBytes], "")
	}
	return msg
}
