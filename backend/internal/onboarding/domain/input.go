package domain

// Pure builders for the generation input (spec 7.10.2): excerpts of the root files and the
// first-task signals. Nothing here reads a file: the application layer passes the decoded text
// in. Secrets never leave this file: .env.example yields NAMES only, README and compose are
// redacted before they are cut. A port of Node's onboarding/domain/input.ts with JavaScript
// semantics (UTF-16 lengths, JSON.parse, toLowerCase).

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	rdomain "dev-digest-go/backend/internal/reviewer/domain"
)

// The input limits (Node constants.ts).
const (
	// ReadmeMaxUnits and ComposeMaxUnits cap the excerpts in UTF-16 units.
	ReadmeMaxUnits  = 16 * 1024
	ComposeMaxUnits = 8 * 1024
	// ScriptsMaxUnits caps the rendered package.json scripts block.
	ScriptsMaxUnits = 4_000
	// EnvNamesMax caps the .env.example variable names.
	EnvNamesMax = 100
	// TodoMax caps the TODO lines of the whole prompt; TodoLineMaxUnits each line.
	TodoMax          = 20
	TodoLineMaxUnits = 160
	// UntestedMax caps the untested-file signal.
	UntestedMax = 10
	// TopFilesInPrompt is the number of ranked files listed in the prompt (the service passes
	// the first TopFilesInPrompt of the TopFilesLimit ranked paths it read).
	TopFilesInPrompt = 40
)

// The root files read at the indexed commit (spec 7.10.1).
const (
	// PackageJSONPath is the file whose scripts block is sent.
	PackageJSONPath = "package.json"
	// EnvExamplePath is the file whose variable names are sent.
	EnvExamplePath = ".env.example"
)

// RootFilePaths is the root read list, in the order PickRootFiles expects the texts: the README
// candidates (README.md, README, readme.md), package.json, the compose candidates
// (docker-compose.yml, docker-compose.yaml, compose.yml, compose.yaml) and .env.example.
func RootFilePaths() []string {
	return []string{
		"README.md", "README", "readme.md", PackageJSONPath,
		"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml", EnvExamplePath,
	}
}

// RootTexts are the root files by role; nil means the file does not exist at the indexed commit
// (an existing empty file is a non-nil empty string).
type RootTexts struct {
	Readme, PackageJSON, Compose, EnvExample *string
}

// PickRootFiles assigns the texts read for RootFilePaths (same order, nil = absent): the README
// and the compose file are the first candidate that exists, even when its content is empty.
func PickRootFiles(texts []*string) RootTexts {
	at := func(i int) *string {
		if i < len(texts) {
			return texts[i]
		}
		return nil
	}
	first := func(from, count int) *string {
		for i := from; i < from+count; i++ {
			if t := at(i); t != nil {
				return t
			}
		}
		return nil
	}
	return RootTexts{Readme: first(0, 3), PackageJSON: at(3), Compose: first(4, 4), EnvExample: at(8)}
}

// TodoLine is one TODO or FIXME line of a scanned file.
type TodoLine struct {
	Path string
	// Line is 1-based.
	Line int
	// Text is the line cut to TodoLineMaxUnits.
	Text string
}

// ReadmeExcerpt is the README as sent to the model: secrets redacted, trimmed, cut to
// ReadmeMaxUnits. ok is false when nothing is left.
func ReadmeExcerpt(text string) (excerpt string, ok bool) {
	return redactedExcerpt(text, ReadmeMaxUnits)
}

// ComposeExcerpt is the compose file as sent to the model (same steps, ComposeMaxUnits).
func ComposeExcerpt(text string) (excerpt string, ok bool) {
	return redactedExcerpt(text, ComposeMaxUnits)
}

func redactedExcerpt(text string, max int) (string, bool) {
	t := rdomain.JSTrim(RedactSecrets(text))
	if t == "" {
		return "", false
	}
	return rdomain.PrefixUnits(t, max), true
}

// eachLine calls fn with every line of text as JavaScript's text.split(/\r?\n/) yields it
// (1-based number), without allocating the list; fn returns false to stop. A line ended by "\n"
// loses one "\r" before it; the last line keeps whatever it ends with.
func eachLine(text string, fn func(n int, line string) bool) {
	n := 0
	for {
		n++
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			fn(n, text)
			return
		}
		line := text[:i]
		line = strings.TrimSuffix(line, "\r")
		if !fn(n, line) {
			return
		}
		text = text[i+1:]
	}
}

var envNameRE = regexp.MustCompile(`^` + jsSpaceRE + `*(?:export` + jsSpaceRE + `+)?([A-Za-z_][A-Za-z0-9_]*)` + jsSpaceRE + `*=`)

// EnvVariableNames returns the variable NAMES of a .env.example: unique, in order, at most
// EnvNamesMax. Values and comments are discarded.
func EnvVariableNames(text string) []string {
	names := []string{}
	seen := map[string]bool{}
	eachLine(text, func(_ int, line string) bool {
		if m := envNameRE.FindStringSubmatch(line); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
		return len(names) < EnvNamesMax
	})
	return names
}

var todoRE = regexp.MustCompile(`\b(?:TODO|FIXME)\b`)

// FindTodoLines returns the TODO/FIXME lines of one file (1-based line numbers), each cut to
// TodoLineMaxUnits; limit > 0 stops after that many (the prompt keeps TodoMax overall, so no
// file needs more). The returned text does not reference text: a 512 KiB blob can be dropped as
// soon as this returns.
func FindTodoLines(path, text string, limit int) []TodoLine {
	var out []TodoLine
	eachLine(text, func(n int, line string) bool {
		if (strings.Contains(line, "TODO") || strings.Contains(line, "FIXME")) && todoRE.MatchString(line) {
			out = append(out, TodoLine{Path: path, Line: n, Text: strings.Clone(rdomain.Cut(line, TodoLineMaxUnits))})
		}
		return limit <= 0 || len(out) < limit
	})
	return out
}

// CollectTodoLines flattens the per-file TODO lines in rank order, capped at TodoMax overall.
func CollectTodoLines(perFile [][]TodoLine) []TodoLine {
	out := []TodoLine{}
	for _, lines := range perFile {
		for _, l := range lines {
			if len(out) == TodoMax {
				return out
			}
			out = append(out, l)
		}
	}
	return out
}

var testPathRE = regexp.MustCompile(`(?:[._-](?:` + ci("test") + `|` + ci("spec") + `)\.[^/]+$)|(?:(?:^|/)(?:` + ci("__tests__") + `|` + ci("tests?") + `)/)`)

// IsTestPath reports whether a repo-relative path is a test file or lies in a test folder.
func IsTestPath(path string) bool { return testPathRE.MatchString(path) }

// jsLower is JavaScript's String.prototype.toLowerCase: the simple lower-case mapping, except
// U+0130 (becomes "i" and U+0307) and the Greek capital sigma, which becomes the final sigma at
// the end of a word.
func jsLower(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return asciiLower(s)
	}
	rs := []rune(s)
	var sb strings.Builder
	for i, r := range rs {
		switch r {
		case 0x130:
			sb.WriteString("i\u0307")
		case 0x3a3:
			if finalSigma(rs, i) {
				sb.WriteRune('ς')
			} else {
				sb.WriteRune('σ')
			}
		default:
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}

func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func isCaseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', 0xa8, 0xad, 0xaf, 0xb4, 0xb7, 0xb8, 0x387, 0x55f, 0x5f4, 0x2018, 0x2019, 0x2024, 0x2027, 0xfe13, 0xfe52, 0xfe55, 0xff07, 0xff0e, 0xff1a:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// finalSigma: the sigma at rs[i] follows a cased letter and is not followed by one (case-ignorable
// characters in between are skipped).
func finalSigma(rs []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		before = isCased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if isCaseIgnorable(rs[j]) {
			continue
		}
		return !isCased(rs[j])
	}
	return true
}

// FindUntestedFiles returns up to UntestedMax top-ranked source files with no indexed test file
// whose lower-cased name contains their lower-cased base name (the file name without its last
// extension; a leading-dot name keeps its dot). rankedPaths is rank-descending; test paths are
// never candidates; an empty base or a path already seen in a test name is skipped.
func FindUntestedFiles(rankedPaths, indexedPaths []string) []string {
	var testNames []string
	for _, p := range indexedPaths {
		if IsTestPath(p) {
			testNames = append(testNames, jsLower(p[strings.LastIndexByte(p, '/')+1:]))
		}
	}
	out := []string{}
	for _, p := range rankedPaths {
		if IsTestPath(p) {
			continue
		}
		file := p[strings.LastIndexByte(p, '/')+1:]
		if dot := strings.LastIndexByte(file, '.'); dot > 0 {
			file = file[:dot]
		}
		base := jsLower(file)
		if base == "" || containsAny(testNames, base) {
			continue
		}
		out = append(out, p)
		if len(out) >= UntestedMax {
			break
		}
	}
	return out
}

func containsAny(names []string, sub string) bool {
	for _, n := range names {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// --- package.json scripts ------------------------------------------------------------

// PackageScripts returns the "scripts" block of a package.json re-rendered as JSON.stringify
// (value, null, 2) would, cut to ScriptsMaxUnits: only string values, keys in JavaScript object
// order (array-index keys ascending first, then first-insertion order; a repeated key keeps its
// first position and its last value). ok is false when the text is not JSON (JSON.parse
// semantics), is not an object, or has no scripts object with a string value.
func PackageScripts(text string) (rendered string, ok bool) {
	p := &jsonScan{s: text}
	p.ws()
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return "", false
	}
	p.i++
	var scripts *scriptList
	p.ws()
	if p.peek() == '}' {
		p.i++
	} else {
		for {
			p.ws()
			key, ok := p.str()
			if !ok {
				return "", false
			}
			p.ws()
			if p.peek() != ':' {
				return "", false
			}
			p.i++
			p.ws()
			if key == "scripts" {
				var ok bool
				if scripts, ok = p.scriptsValue(); !ok {
					return "", false
				}
			} else if !p.skipValue() {
				return "", false
			}
			p.ws()
			c := p.peek()
			p.i++
			if c == ',' {
				continue
			}
			if c == '}' {
				break
			}
			return "", false
		}
	}
	p.ws()
	if p.i != len(p.s) || scripts == nil {
		return "", false
	}
	entries := scripts.ordered()
	if len(entries) == 0 {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString("{\n")
	for i, e := range entries {
		sb.WriteString("  ")
		writeJSONString(&sb, e.key)
		sb.WriteString(": ")
		writeJSONString(&sb, e.val)
		if i < len(entries)-1 {
			sb.WriteByte(',')
		}
		sb.WriteByte('\n')
	}
	sb.WriteByte('}')
	return rdomain.PrefixUnits(sb.String(), ScriptsMaxUnits), true
}

type scriptEntry struct {
	key, val string
	isStr    bool
}

type scriptList struct {
	entries []scriptEntry
	at      map[string]int
}

func (l *scriptList) set(key, val string, isStr bool) {
	if i, ok := l.at[key]; ok {
		l.entries[i].val, l.entries[i].isStr = val, isStr
		return
	}
	l.at[key] = len(l.entries)
	l.entries = append(l.entries, scriptEntry{key, val, isStr})
}

// ordered returns the string-valued entries in JavaScript property order.
func (l *scriptList) ordered() []scriptEntry {
	var idx, rest []scriptEntry
	for _, e := range l.entries {
		if !e.isStr {
			continue
		}
		if _, ok := arrayIndex(e.key); ok {
			idx = append(idx, e)
		} else {
			rest = append(rest, e)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, _ := arrayIndex(idx[i].key)
		b, _ := arrayIndex(idx[j].key)
		return a < b
	})
	return append(idx, rest...)
}

// arrayIndex reports whether k is a canonical array index (decimal, no leading zero, below
// 2^32-1), the keys a JavaScript object enumerates first.
func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return 0, false
		}
	}
	n, _ := strconv.ParseUint(k, 10, 64)
	return n, n < 1<<32-1
}

// jsonScan is a strict JSON reader with JSON.parse semantics (whitespace is space, tab, LF, CR;
// no BOM, no trailing commas, no leading zeros, no nesting limit). Strings decode like
// JavaScript's: a lone surrogate escape is kept as its three WTF-8 bytes so JSON.stringify's
// \udXXX output can be reproduced.
type jsonScan struct {
	s string
	i int
}

func (p *jsonScan) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *jsonScan) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func hexDigit(c byte) (uint32, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint32(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint32(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return uint32(c-'A') + 10, true
	}
	return 0, false
}

// hex4 reads the four hex digits of a \u escape at p.i.
func (p *jsonScan) hex4() (uint32, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	var v uint32
	for k := 0; k < 4; k++ {
		d, ok := hexDigit(p.s[p.i+k])
		if !ok {
			return 0, false
		}
		v = v<<4 | d
	}
	p.i += 4
	return v, true
}

func appendWTF8(b []byte, cp uint32) []byte {
	if cp >= 0xd800 && cp <= 0xdfff {
		return append(b, byte(0xe0|cp>>12), byte(0x80|(cp>>6)&0x3f), byte(0x80|cp&0x3f))
	}
	return utf8.AppendRune(b, rune(cp))
}

// str reads a string literal at p.i.
func (p *jsonScan) str() (string, bool) {
	if p.peek() != '"' {
		return "", false
	}
	p.i++
	start := p.i
	// fast path: no escape
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' {
			s := p.s[start:p.i]
			p.i++
			return s, true
		}
		if c < 0x20 {
			return "", false
		}
		if c == '\\' {
			break
		}
		p.i++
	}
	buf := []byte(p.s[start:p.i])
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(buf), true
		case c < 0x20:
			return "", false
		case c != '\\':
			buf = append(buf, c)
			p.i++
		default:
			p.i++
			if p.i >= len(p.s) {
				return "", false
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case '"', '\\', '/':
				buf = append(buf, e)
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				cp, ok := p.hex4()
				if !ok {
					return "", false
				}
				if cp >= 0xd800 && cp <= 0xdbff && p.i+1 < len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
					save := p.i
					p.i += 2
					if lo, ok := p.hex4(); ok && lo >= 0xdc00 && lo <= 0xdfff {
						cp = 0x10000 + (cp-0xd800)<<10 + (lo - 0xdc00)
					} else {
						p.i = save
					}
				}
				buf = appendWTF8(buf, cp)
			default:
				return "", false
			}
		}
	}
	return "", false
}

// scriptsValue reads the value of a "scripts" member: an object becomes a scriptList (nil
// result otherwise), validated like any value.
func (p *jsonScan) scriptsValue() (*scriptList, bool) {
	if p.peek() != '{' {
		return nil, p.skipValue()
	}
	p.i++
	l := &scriptList{at: map[string]int{}}
	p.ws()
	if p.peek() == '}' {
		p.i++
		return l, true
	}
	for {
		p.ws()
		key, ok := p.str()
		if !ok {
			return nil, false
		}
		p.ws()
		if p.peek() != ':' {
			return nil, false
		}
		p.i++
		p.ws()
		if p.peek() == '"' {
			v, ok := p.str()
			if !ok {
				return nil, false
			}
			l.set(key, v, true)
		} else {
			if !p.skipValue() {
				return nil, false
			}
			l.set(key, "", false)
		}
		p.ws()
		c := p.peek()
		p.i++
		if c == ',' {
			continue
		}
		if c == '}' {
			return l, true
		}
		return nil, false
	}
}

// skipValue validates and consumes one value, iteratively (no recursion limit).
func (p *jsonScan) skipValue() bool {
	var stack []byte
	for {
		p.ws()
		if p.i >= len(p.s) {
			return false
		}
		switch c := p.s[p.i]; {
		case c == '{':
			p.i++
			p.ws()
			if p.peek() == '}' {
				p.i++
				break
			}
			stack = append(stack, '{')
			if !p.skipKey() {
				return false
			}
			continue
		case c == '[':
			p.i++
			p.ws()
			if p.peek() == ']' {
				p.i++
				break
			}
			stack = append(stack, '[')
			continue
		case c == '"':
			if _, ok := p.str(); !ok {
				return false
			}
		case c == '-' || (c >= '0' && c <= '9'):
			if !p.number() {
				return false
			}
		case strings.HasPrefix(p.s[p.i:], "true"):
			p.i += 4
		case strings.HasPrefix(p.s[p.i:], "false"):
			p.i += 5
		case strings.HasPrefix(p.s[p.i:], "null"):
			p.i += 4
		default:
			return false
		}
		// a value is complete: close containers or move to the next element
		for {
			if len(stack) == 0 {
				return true
			}
			p.ws()
			c := p.peek()
			top := stack[len(stack)-1]
			switch {
			case c == ',':
				p.i++
				if top == '{' {
					p.ws()
					if !p.skipKey() {
						return false
					}
				}
			case (top == '{' && c == '}') || (top == '[' && c == ']'):
				p.i++
				stack = stack[:len(stack)-1]
				continue
			default:
				return false
			}
			break
		}
	}
}

// skipKey consumes `"key" :`.
func (p *jsonScan) skipKey() bool {
	if _, ok := p.str(); !ok {
		return false
	}
	p.ws()
	if p.peek() != ':' {
		return false
	}
	p.i++
	return true
}

// number consumes a JSON number: -?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?
func (p *jsonScan) number() bool {
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.peek() == '-' {
		p.i++
	}
	switch c := p.peek(); {
	case c == '0':
		p.i++
	case c >= '1' && c <= '9':
		digits()
	default:
		return false
	}
	if p.peek() == '.' {
		p.i++
		if digits() == 0 {
			return false
		}
	}
	if c := p.peek(); c == 'e' || c == 'E' {
		p.i++
		if c := p.peek(); c == '+' || c == '-' {
			p.i++
		}
		if digits() == 0 {
			return false
		}
	}
	return true
}

// writeJSONString writes s as JSON.stringify does (well-formed): only the quote, the backslash
// and the control characters are escaped, a lone surrogate (three WTF-8 bytes) as \udXXX.
func writeJSONString(sb *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	sb.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			sb.WriteString(`\"`)
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\b':
			sb.WriteString(`\b`)
		case c == '\f':
			sb.WriteString(`\f`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteByte(hex[c>>4])
			sb.WriteByte(hex[c&0xf])
		case c < utf8.RuneSelf:
			sb.WriteByte(c)
		case c == 0xed && i+2 < len(s) && s[i+1] >= 0xa0 && s[i+1] <= 0xbf && s[i+2]&0xc0 == 0x80:
			cp := 0xd000 | uint32(s[i+1]&0x3f)<<6 | uint32(s[i+2]&0x3f)
			sb.WriteString(`\u`)
			for shift := 12; shift >= 0; shift -= 4 {
				sb.WriteByte(hex[cp>>uint(shift)&0xf])
			}
			i += 3
			continue
		default:
			r, w := utf8.DecodeRuneInString(s[i:])
			sb.WriteRune(r) // an invalid byte writes U+FFFD
			i += w
			continue
		}
		i++
	}
	sb.WriteByte('"')
}
