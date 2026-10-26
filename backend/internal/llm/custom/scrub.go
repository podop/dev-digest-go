package custom

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// minFragment is the shortest run of a secret that Scrub masks inside gateway text.
const minFragment = 4

var (
	// keyLikeRun matches provider key shapes (sk-…, sk-or-…, sk-ant-…, sk-proj-…), masked or not.
	keyLikeRun = regexp.MustCompile(`(?i)\bsk-[\w*•…-]*`)
	// maskedRun matches any word run holding a mask (two or more of * • …): gateways echo a
	// masked copy of a rejected key that an exact match misses.
	maskedRun = regexp.MustCompile(`[\w-]*[*•…]{2,}[\w-]*`)
)

// Scrub replaces with mask everything in s that is, or reveals part of, one of secrets (spec
// NFR1: no key or header value "or part of one"): each whole secret longest first, also in its
// JSON-escaped and %q-quoted forms (Go's HTTP/2 stack quotes header values in its errors),
// key-shaped and masked runs, and every run of at least four bytes that is a substring of a
// secret. s is the text of an error or of a gateway message, never a prompt or a model answer: the
// fragment pass masks ordinary words that happen to occur inside a secret. mask must not be empty.
func Scrub(s string, secrets []string, mask string) string {
	secrets = sortedSecrets(secrets)
	s = scrubWhole(s, secrets, mask)
	for _, sec := range secrets {
		s = maskFragments(s, sec, mask)
	}
	return s
}

// ScrubWhole is Scrub without the fragment pass. It keeps the structure of a JSON document
// intact (a fragment can be a quote or a key name), so it is the one for a raw response body that
// is parsed afterwards; the extracted message then goes through Scrub.
func ScrubWhole(s string, secrets []string, mask string) string {
	return scrubWhole(s, sortedSecrets(secrets), mask)
}

func sortedSecrets(secrets []string) []string {
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(sec string) bool { return sec == "" })
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	return secrets
}

func scrubWhole(s string, secrets []string, mask string) string {
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, mask)
		if q, err := json.Marshal(sec); err == nil {
			if esc := string(q[1 : len(q)-1]); esc != sec {
				s = strings.ReplaceAll(s, esc, mask)
			}
		}
		if q := strconv.Quote(sec); len(q) >= 2 {
			if esc := q[1 : len(q)-1]; esc != sec {
				s = strings.ReplaceAll(s, esc, mask)
			}
		}
	}
	s = keyLikeRun.ReplaceAllString(s, mask)
	return maskedRun.ReplaceAllString(s, mask)
}

// maskFragments replaces every run of s of at least minFragment bytes that occurs in secret. The
// mask itself is left alone.
func maskFragments(s, secret, mask string) string {
	if len(secret) < minFragment {
		return s
	}
	parts := strings.Split(s, mask)
	for i, p := range parts {
		parts[i] = maskRuns(p, secret, mask)
	}
	return strings.Join(parts, mask)
}

func maskRuns(s, secret, mask string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		n := 0
		if i+minFragment <= len(s) && strings.Contains(secret, s[i:i+minFragment]) {
			n = minFragment
			for i+n < len(s) && strings.Contains(secret, s[i:i+n+1]) {
				n++
			}
		}
		if n == 0 {
			out.WriteByte(s[i])
			i++
			continue
		}
		out.WriteString(mask)
		i += n
	}
	return out.String()
}
