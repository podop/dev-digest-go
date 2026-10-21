// Package logging builds the structured JSON logger used by the backend and
// redacts credentials from every record.
package logging

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Redacted replaces the value of every sensitive field.
const Redacted = "[REDACTED]"

// sensitive holds attribute and header names (lowercased, without '-' and '_')
// whose values are never logged.
var sensitive = map[string]bool{
	"authorization": true,
	"cookie":        true,
	"xapikey":       true,
	"key":           true,
	"token":         true,
	"secret":        true,
	"password":      true,
	"apikey":        true,
}

// New returns a logger that writes one JSON object per line to w at the given
// level or above, with sensitive fields redacted.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewHandler(w, level))
}

// NewHandler is New for callers that need the slog.Handler.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr})
}

func isSensitive(name string) bool {
	n := strings.ToLower(name)
	n = strings.ReplaceAll(n, "-", "")
	n = strings.ReplaceAll(n, "_", "")
	return sensitive[n]
}

func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if isSensitive(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	if a.Value.Kind() == slog.KindAny {
		switch v := a.Value.Any().(type) {
		case http.Header:
			return slog.Any(a.Key, redactHeader(v))
		case map[string]string:
			return slog.Any(a.Key, redactStrings(v))
		case map[string]any:
			return slog.Any(a.Key, redactAny(v))
		}
	}
	return a
}

func redactHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vals := range h {
		if isSensitive(k) {
			out[k] = []string{Redacted}
			continue
		}
		out[k] = vals
	}
	return out
}

func redactStrings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if isSensitive(k) {
			v = Redacted
		}
		out[k] = v
	}
	return out
}

func redactAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch {
		case isSensitive(k):
			v = Redacted
		default:
			if nested, ok := v.(map[string]any); ok {
				v = redactAny(nested)
			}
		}
		out[k] = v
	}
	return out
}
