package gitlab

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// mask replaces a token, or a part of one, in any text that may reach a log line or a message.
const mask = "[REDACTED]"

// minFragment is the shortest run of the token that scrub masks on its own. GitLab tokens are
// random above their prefix, so an 8-byte run is never an ordinary word.
const minFragment = 8

// gitUser is the user name git sends with a GitLab token (platform/gitcli builds its Basic
// credential from the same pair).
const gitUser = "oauth2"

// TokenShape matches the GitLab token prefixes of spec FR31 (glpat, glptt, glrt, glrtr, gldt, glcbt,
// glagent, glft, glimt, glsoat, glffct, gloas) followed by their random part; the routable form
// (glpat-<random>.01.<crc>) has dots, so the class allows them. A token of another connection, or
// one a proxy echoes, is masked too. gitlabconn/app masks its messages with the same expression.
var TokenShape = regexp.MustCompile(`(?i)\bgl(?:pat|oas|dt|rt|rtr|cbt|ptt|ft|imt|agent|soat|ffct)-[\w.-]{8,}`)

// scrub drops token, and anything that reveals part of it, from s: the whole token in its plain,
// JSON-escaped, quoted and URL-escaped forms, the base64 HTTP Basic credential git sends to the
// same host, token-shaped runs, and every run of at least
// minFragment bytes that occurs inside the token (NFR1: "no token, or part of one"). Redaction
// must run before any truncation of s. An empty token leaves s unchanged apart from token-shaped
// runs.
func scrub(s, token string) string {
	if token != "" {
		for _, form := range tokenForms(token) {
			s = strings.ReplaceAll(s, form, mask)
		}
	}
	s = TokenShape.ReplaceAllString(s, mask)
	if len(token) >= minFragment {
		parts := strings.Split(s, mask)
		for i, p := range parts {
			parts[i] = maskRuns(p, token)
		}
		s = strings.Join(parts, mask)
	}
	return s
}

func tokenForms(token string) []string {
	forms := []string{token, url.QueryEscape(token), url.PathEscape(token)}
	if q, err := json.Marshal(token); err == nil && len(q) >= 2 {
		forms = append(forms, string(q[1:len(q)-1]))
	}
	if q := strconv.Quote(token); len(q) >= 2 {
		forms = append(forms, q[1:len(q)-1])
	}
	// A server may echo the Basic credential git sends (a proxy page, a reflected header).
	forms = append(forms, base64.StdEncoding.EncodeToString([]byte(gitUser+":"+token)))
	// Longest first, so a form that contains another is not left half-masked.
	for i := 1; i < len(forms); i++ {
		for j := i; j > 0 && len(forms[j]) > len(forms[j-1]); j-- {
			forms[j], forms[j-1] = forms[j-1], forms[j]
		}
	}
	return forms
}

func maskRuns(s, token string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		n := 0
		if i+minFragment <= len(s) && strings.Contains(token, s[i:i+minFragment]) {
			n = minFragment
			for i+n < len(s) && strings.Contains(token, s[i:i+n+1]) {
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
