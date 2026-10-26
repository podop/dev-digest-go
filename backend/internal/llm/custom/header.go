package custom

import (
	"fmt"
	"strings"
)

// Limits of the extra headers (spec FR4).
const (
	// MaxHeaders is the most extra headers a provider can have.
	MaxHeaders = 5
	// MaxHeaderNameLen is the longest header name.
	MaxHeaderNameLen = 64
	// MaxHeaderValueLen is the longest header value.
	MaxHeaderValueLen = 4096
)

// reservedHeaders are never allowed as an extra header (lower case).
var reservedHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"content-type":      {},
	"transfer-encoding": {},
	"connection":        {},
	"authorization":     {},
	"cookie":            {},
}

// CheckHeaderName validates the name of an extra header and returns "" when it is acceptable,
// else the message for the 422. authHeader is the provider's configured auth header (empty when
// there is none, e.g. bearer auth); the name must differ from it case-insensitively.
//
// Rules: an HTTP token (RFC 9110 tchar) of 1-64 characters, and not Host, Content-Length,
// Content-Type, Transfer-Encoding, Connection, Authorization or Cookie. Uniqueness among the
// headers and the count limit are the caller's.
func CheckHeaderName(name, authHeader string) string {
	if n := len(name); n < 1 || n > MaxHeaderNameLen {
		return fmt.Sprintf("Header name must be 1-%d characters", MaxHeaderNameLen)
	}
	if !IsToken(name) {
		return "Header name must be a valid HTTP token"
	}
	lower := strings.ToLower(name)
	if _, bad := reservedHeaders[lower]; bad {
		return "Header name is reserved"
	}
	if authHeader != "" && strings.EqualFold(name, authHeader) {
		return "Header name duplicates the auth header"
	}
	return ""
}

// CheckHeaderValue validates the value of an extra header and returns "" when acceptable:
// 1-4,096 characters without CR, LF or NUL.
func CheckHeaderValue(value string) string {
	if n := len(value); n < 1 || n > MaxHeaderValueLen {
		return fmt.Sprintf("Header value must be 1-%d characters", MaxHeaderValueLen)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "Header value must not contain CR, LF or NUL"
	}
	return ""
}

// IsToken reports whether s is a non-empty HTTP token (RFC 9110 tchar only).
func IsToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTchar(s[i]) {
			return false
		}
	}
	return true
}

func isTchar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}
