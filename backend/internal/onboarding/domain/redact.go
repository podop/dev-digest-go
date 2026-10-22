package domain

// Secret redaction for repository text that is sent to a third-party model (README, compose
// file): a port of Node's redactSecrets (onboarding/domain/redact.ts) with JavaScript regular
// expression semantics (spec 7.10.2, D5010). Three passes: (a) the password of a
// scheme://user:PASS@host URL, (b) any secret-looking token, (c) per line, the value of a
// KEY: value / KEY=value pair whose key is secret, at the start of the line (the whole rest of
// the line plus the body of a | or > block scalar) or anywhere on it. The key stays, so the model
// still sees that the variable exists. A key is secret by whole segments (split on _ - . and
// camelCase), not by substring: max_tokens, token_budget, authentication and author stay.
//
// RE2 differs from JavaScript here in three ways, each handled below: \s, \S and . are ASCII or
// narrower than JavaScript's (the jsSpace and jsDot classes spell the JavaScript sets out); the
// (?i) flag folds U+017F and U+212A into "s" and "k" where JavaScript's /i does not (every
// case-insensitive literal is spelled out with ci); and the one look-behind and the one
// back-reference of the embedded-pair pattern are matched by hand (matchEmbeddedPair).

import (
	"regexp"
	"strings"
	"unicode/utf8"

	rdomain "dev-digest-go/backend/internal/reviewer/domain"
)

// Redacted replaces a secret value.
const Redacted = "<redacted>"

const (
	// jsSpaceChars is the body of JavaScript's \s: WhiteSpace and LineTerminator code points.
	jsSpaceChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	jsSpaceRE    = `[` + jsSpaceChars + `]`  // \s
	jsNotSpaceRE = `[^` + jsSpaceChars + `]` // \S
	// jsDotRE is JavaScript's `.`: anything but the four line terminators.
	jsDotRE = `[^\n\r\x{2028}\x{2029}]`
)

// ci spells a literal of ASCII letters and punctuation as a case-insensitive regexp, folding
// ASCII letters only (JavaScript /i without the u flag folds nothing else).
func ci(lit string) string {
	var sb strings.Builder
	for i := 0; i < len(lit); i++ {
		c := lit[i]
		switch {
		case c >= 'a' && c <= 'z':
			sb.WriteString("[" + string(c) + string(c-'a'+'A') + "]")
		case c >= 'A' && c <= 'Z':
			sb.WriteString("[" + string(c-'A'+'a') + string(c) + "]")
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// asciiLower lower-cases ASCII letters only.
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// leadingSpace returns the byte length and the UTF-16 length of the JavaScript whitespace the
// string starts with (/^\s*/).
func leadingSpace(s string) (bytes, units int) {
	for i, r := range s {
		if !isJSSpace(r) {
			return i, units
		}
		units++ // every JavaScript whitespace character is in the BMP
	}
	return len(s), units
}

var (
	strongWords     = setOf("password", "passwd", "passphrase", "pass", "secret", "credential", "credentials", "authorization", "bearer", "apikey")
	strongSuffixes  = []string{"password", "passwd", "passphrase", "secret", "apikey"}
	tokenBenign     = setOf("budget", "limit", "count", "max", "min", "total", "size", "length", "ttl", "expiry", "expires", "usage", "type", "url", "endpoint", "path", "file")
	keyPrefixes     = setOf("access", "private", "secret", "client", "api", "auth", "signing", "encryption", "master", "session", "jwt", "ssh", "deploy")
	camelLowerUpper = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	camelAcronym    = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	keySeparators   = regexp.MustCompile(`[ _.-]+`)
)

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

type strength int

const (
	notSecret strength = iota
	// strong: redact any non-reference value.
	strong
	// weak: a token or key whose numeric value is not a secret.
	weak
)

// segmentsOf splits a key (ASCII: the patterns only let ASCII keys through) into lower-case
// segments at _ - . and camelCase boundaries.
func segmentsOf(key string) []string {
	k := camelLowerUpper.ReplaceAllString(key, "$1 $2")
	k = camelAcronym.ReplaceAllString(k, "$1 $2")
	var segs []string
	for _, s := range keySeparators.Split(asciiLower(k), -1) {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

func classifyKey(key string) strength {
	segs := segmentsOf(key)
	for _, s := range segs {
		if strongWords[s] {
			return strong
		}
		for _, x := range strongSuffixes {
			if strings.HasSuffix(s, x) {
				return strong
			}
		}
	}
	hasToken, benign := false, false
	for _, s := range segs {
		hasToken = hasToken || strings.HasSuffix(s, "token")
		benign = benign || tokenBenign[s]
	}
	if hasToken && !benign {
		return weak
	}
	for i, s := range segs {
		if s == "key" {
			for _, p := range segs[:i] {
				if keyPrefixes[p] {
					return weak
				}
			}
			break // indexOf('key'): only the first one counts
		}
	}
	return notSecret
}

var (
	numberRE = regexp.MustCompile(`^[+-]?\d+(?:\.\d+)?$`)
	// ${VAR} / ${VAR:-x} or $UPPER_NAME: a reference, not a literal. `$ecret` is a literal.
	envRefRE = regexp.MustCompile(`^(?:\$\{[A-Za-z_][^}` + jsSpaceChars + `]*\}|\$[A-Z_][A-Z0-9_]*)$`)
	// A line that only opens a collection (`"credentials": {`): the secrets are on the lines below.
	openerRE      = regexp.MustCompile(`^[\[{]` + jsSpaceRE + `*[\]}]?,?$`)
	blockScalarRE = regexp.MustCompile(`^[|>][+-]?\d*[+-]?` + jsSpaceRE + `*(?:#` + jsDotRE + `*)?$`)
	booleanWords  = setOf("true", "false", "yes", "no", "on", "off", "null", "~")
)

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func isRedactable(s strength, rawValue string) bool {
	v := unquote(rdomain.JSTrim(rawValue))
	if v == "" || v == Redacted || openerRE.MatchString(v) || booleanWords[asciiLower(v)] || envRefRE.MatchString(v) {
		return false
	}
	if s == weak && numberRE.MatchString(v) {
		return false
	}
	return true
}

var (
	// prefix (indent, `- `, `export `) · optional quote · key · same quote · `:`/`=` · value. The
	// back-reference of the JavaScript pattern is the three alternatives: the quote character is
	// decided by the character after the prefix, which no prefix backtracking can change.
	keyValueLineRE = regexp.MustCompile(`^(` + jsSpaceRE + `*(?:-` + jsSpaceRE + `+)?(?:export` + jsSpaceRE + `+)?)` +
		`(?:"([A-Za-z_][\w.-]*)"|'([A-Za-z_][\w.-]*)'|([A-Za-z_][\w.-]*))` +
		`(` + jsSpaceRE + `*[:=]` + jsSpaceRE + `*)(` + jsDotRE + `*)$`)

	// scheme://user:PASSWORD@host: only the password part.
	urlUserinfoRE = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*://[^` + jsSpaceChars + `:@/]*:)([^` + jsSpaceChars + `@/]+)@`)

	tokenREs = []*regexp.Regexp{
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}`),
		regexp.MustCompile(`\b[sp]k_(?:live|test)_[A-Za-z0-9]{8,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
	}
)

func redactURLUserinfo(text string) string {
	idx := urlUserinfoRE.FindAllStringSubmatchIndex(text, -1)
	if idx == nil {
		return text
	}
	var sb strings.Builder
	last := 0
	for _, m := range idx {
		head, password := text[m[2]:m[3]], text[m[4]:m[5]]
		if password == Redacted || envRefRE.MatchString(password) {
			continue
		}
		sb.WriteString(text[last:m[0]])
		sb.WriteString(head)
		sb.WriteString(Redacted)
		sb.WriteByte('@')
		last = m[1]
	}
	sb.WriteString(text[last:])
	return sb.String()
}

// pairMatch is one match of the embedded key/value pattern
//
//	(?<![\w.])(["']?)([A-Za-z_][\w.-]*)\1(\s*[:=]\s*)("(?:[^"\\\n]|\\.)*"|'[^'\n]*'|\$\{[^}\s]*\}|(?:(?:Bearer|Basic)\s+)?[^\s,{}[\]"']+)
//
// with the /gi flags (ASCII folding), as byte offsets into the line.
type pairMatch struct {
	start, end         int
	quote              byte // 0, '"' or '\''
	key                string
	sep                string
	valueStart, valEnd int
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isKeyStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// skipSpace returns the offset after the JavaScript whitespace starting at i.
func skipSpace(s string, i int) int {
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !isJSSpace(r) {
			return i
		}
		i += w
	}
	return i
}

// matchEmbeddedPair tries the pattern at byte offset p. On failure skip is the next offset worth
// trying: p+1, or the end of the key when an unquoted key run was scanned (every start inside the
// run ends at the same place and fails the same way, which keeps a long a-a-a-... line linear).
func matchEmbeddedPair(s string, p int) (m pairMatch, ok bool, skip int) {
	if p > 0 && (isWordByte(s[p-1]) || s[p-1] == '.') { // (?<![\w.])
		return m, false, p + 1
	}
	j := p
	var q byte
	if s[j] == '"' || s[j] == '\'' {
		q = s[j]
		j++
	}
	if j >= len(s) || !isKeyStart(s[j]) {
		return m, false, p + 1
	}
	k := j + 1
	for k < len(s) && (isWordByte(s[k]) || s[k] == '.' || s[k] == '-') {
		k++
	}
	keyEnd := k
	fail := p + 1
	if q == 0 {
		fail = keyEnd
	}
	if q != 0 {
		if k >= len(s) || s[k] != q {
			return m, false, fail
		}
		k++
	}
	k = skipSpace(s, k)
	if k >= len(s) || (s[k] != ':' && s[k] != '=') {
		return m, false, fail
	}
	k++
	k = skipSpace(s, k)
	sepEnd := k
	end, ok := matchPairValue(s, k)
	if !ok {
		return m, false, fail
	}
	return pairMatch{start: p, end: end, quote: q, key: s[j:keyEnd], sep: s[keyEnd+btoi(q != 0) : sepEnd], valueStart: sepEnd, valEnd: end}, true, 0
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// matchPairValue matches the value alternatives at s[k:] in order and returns the end offset.
func matchPairValue(s string, k int) (int, bool) {
	if k >= len(s) {
		return 0, false
	}
	switch s[k] {
	case '"': // "(?:[^"\\\n]|\\.)*"
		for i := k + 1; i < len(s); {
			switch c := s[i]; c {
			case '"':
				return i + 1, true
			case '\n':
				i = len(s)
			case '\\':
				if i+1 >= len(s) {
					i = len(s)
					break
				}
				r, w := utf8.DecodeRuneInString(s[i+1:])
				if r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 {
					i = len(s) // `.` does not match a line terminator, and the class excludes the backslash
					break
				}
				i += 1 + w
			default:
				i++
			}
		}
	case '\'': // '[^'\n]*'
		for i := k + 1; i < len(s); i++ {
			if s[i] == '\'' {
				return i + 1, true
			}
			if s[i] == '\n' {
				break
			}
		}
	case '$': // \$\{[^}\s]*\}
		if k+1 < len(s) && s[k+1] == '{' {
			for i := k + 2; i < len(s); {
				if s[i] == '}' {
					return i + 1, true
				}
				r, w := utf8.DecodeRuneInString(s[i:])
				if isJSSpace(r) {
					break
				}
				i += w
			}
		}
	}
	// (?:(?:Bearer|Basic)\s+)?[^\s,{}[\]"']+
	for _, word := range [...]string{"bearer", "basic"} {
		if len(s)-k > len(word) && asciiLower(s[k:k+len(word)]) == word {
			after := skipSpace(s, k+len(word))
			if after > k+len(word) {
				if end := scanPairWord(s, after); end > after {
					return end, true
				}
			}
		}
	}
	if end := scanPairWord(s, k); end > k {
		return end, true
	}
	return 0, false
}

// scanPairWord returns the end of the run of [^\s,{}[\]"'] characters starting at i.
func scanPairWord(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ',', '{', '}', '[', ']', '"', '\'':
			return i
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		if isJSSpace(r) {
			return i
		}
		i += w
	}
	return i
}

func redactEmbedded(line string) string {
	var sb strings.Builder
	last := 0
	for p := 0; p < len(line); {
		m, ok, skip := matchEmbeddedPair(line, p)
		if !ok {
			p = skip
			continue
		}
		p = m.end
		s := classifyKey(m.key)
		value := line[m.valueStart:m.valEnd]
		if s == notSecret || !isRedactable(s, value) {
			continue
		}
		var q string
		if value[0] == '"' || value[0] == '\'' {
			q = value[:1]
		}
		quote := ""
		if m.quote != 0 {
			quote = string(m.quote)
		}
		sb.WriteString(line[last:m.start])
		sb.WriteString(quote + m.key + quote + m.sep + q + Redacted + q)
		last = m.end
	}
	if last == 0 {
		return line
	}
	sb.WriteString(line[last:])
	return sb.String()
}

// redactLine redacts one line (without its trailing \r). blockIndent is >= 0 when the line opens
// a block scalar of a secret key: the deeper-indented lines that follow are its body.
func redactLine(line string) (out string, blockIndent int) {
	if m := keyValueLineRE.FindStringSubmatch(line); m != nil {
		key := m[2] + m[3] + m[4]
		quote := ""
		if m[2] != "" {
			quote = `"`
		} else if m[3] != "" {
			quote = "'"
		}
		if s := classifyKey(key); s != notSecret {
			v := rdomain.JSTrim(m[6])
			if blockScalarRE.MatchString(v) {
				_, indent := leadingSpace(line)
				return line, indent
			}
			if isRedactable(s, v) {
				return m[1] + quote + key + quote + m[5] + Redacted, -1
			}
		}
	}
	return redactEmbedded(line), -1
}

func stripCR(l string) string { return strings.TrimSuffix(l, "\r") }

func trailingCR(l string) string {
	if strings.HasSuffix(l, "\r") {
		return "\r"
	}
	return ""
}

// RedactSecrets replaces secret values in text with "<redacted>", exactly as Node's
// redactSecrets does (spec 7.10.2): URL passwords, the seven token patterns, then per line the
// values of secret-keyed pairs and the bodies of their block scalars. "\r" is preserved.
func RedactSecrets(text string) string {
	out := redactURLUserinfo(text)
	for _, re := range tokenREs {
		out = re.ReplaceAllLiteralString(out, Redacted)
	}
	lines := strings.Split(out, "\n")
	result := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line, blockIndent := redactLine(stripCR(raw))
		result = append(result, line+trailingCR(raw))
		if blockIndent < 0 {
			continue
		}
		// Body of a | or > block scalar: every non-blank line indented deeper than its key.
		for i+1 < len(lines) {
			next := lines[i+1]
			body := stripCR(next)
			n, indent := leadingSpace(body)
			blank := rdomain.JSTrim(body) == ""
			if !blank && indent <= blockIndent {
				break
			}
			if blank {
				result = append(result, body+trailingCR(next))
			} else {
				result = append(result, body[:n]+Redacted+trailingCR(next))
			}
			i++
		}
	}
	return strings.Join(result, "\n")
}
