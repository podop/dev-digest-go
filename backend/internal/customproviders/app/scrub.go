package app

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/logging"
)

const (
	// minFragment is the shortest run of a secret that is scrubbed from gateway text.
	minFragment = 4
	// maxMessage bounds a gateway message shown to the user, in bytes.
	maxMessage = 300
)

var (
	// keyLikeRun matches provider key shapes (sk-…, sk-or-…, sk-ant-…, sk-proj-…), masked or not.
	keyLikeRun = regexp.MustCompile(`(?i)\bsk-[\w*•…-]*`)
	// maskedRun matches any word run holding a mask (two or more of * • …): gateways echo a
	// masked copy of the key on a 401.
	maskedRun = regexp.MustCompile(`[\w-]*[*•…]{2,}[\w-]*`)
)

// scrub removes every secret of a provider from text a gateway wrote, then cuts it to
// maxMessage bytes (redact first, cut second: a cut can leave half a secret). It masks each whole
// secret (longest first), key-shaped and masked runs, and every run of at least minFragment bytes
// that is a substring of a secret (spec NFR1: no key or header value "or part of one").
func scrub(msg string, secrets []string) string {
	msg = truncate(strings.TrimSpace(msg), 4*maxMessage)
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(s string) bool { return s == "" })
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, logging.Redacted)
	}
	msg = keyLikeRun.ReplaceAllString(msg, logging.Redacted)
	msg = maskedRun.ReplaceAllString(msg, logging.Redacted)
	for _, s := range secrets {
		msg = maskFragments(msg, s)
	}
	return truncate(msg, maxMessage)
}

// maskFragments replaces every run of msg of at least minFragment bytes that occurs in secret.
func maskFragments(msg, secret string) string {
	if len(secret) < minFragment {
		return msg
	}
	var out strings.Builder
	for i := 0; i < len(msg); {
		n := 0
		if i+minFragment <= len(msg) && strings.Contains(secret, msg[i:i+minFragment]) {
			n = minFragment
			for i+n < len(msg) && strings.Contains(secret, msg[i:i+n+1]) {
				n++
			}
		}
		if n == 0 {
			out.WriteByte(msg[i])
			i++
			continue
		}
		out.WriteString(logging.Redacted)
		i += n
	}
	return out.String()
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
