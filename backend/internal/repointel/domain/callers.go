package domain

import (
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of the CallerSignatures operation (spec section 7.11, D4004).
const (
	// CallerScanTimeout bounds one CallerSignatures call.
	CallerScanTimeout = 30 * time.Second
	// MaxCallerFileBytes is the exclusive size limit of a scanned file: Node scans files below
	// 2,000,000 bytes.
	MaxCallerFileBytes = 2_000_000
	// DefaultCallerLimit is the row limit used when the caller passes none (zero or less).
	DefaultCallerLimit = 20
	// MaxCallerNames bounds the callable names one call scans for; names past it are ignored.
	// Node has no bound; a real diff declares far fewer.
	MaxCallerNames = 1000
	// MaxCallerScanFiles bounds the files one scan lists; the walk stops at it (breadth first).
	MaxCallerScanFiles = 100_000
)

// CallerSignature is one row of the CallerSignatures read: a caller of a changed file's callable
// symbols, labelled with its enclosing top-level symbol and that symbol's signature.
type CallerSignature struct {
	// File is the caller file, slash-separated and relative to the clone.
	File string
	// Symbol is the enclosing symbol of the call site; Signature is its declaration head.
	Symbol    string
	Signature string
	// Rank is the file_rank percentile of File; 0 when the file has none.
	Rank int
}

// IsCallerScanDir reports a directory name the caller scan never enters, at any depth.
func IsCallerScanDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", ".next", "coverage", "vendor", "target":
		return true
	}
	return false
}

// IsCallerScanFile reports whether p has one of the extensions the caller scan reads
// (.ts .tsx .js .jsx .mjs .cjs .go .java, case-sensitive like Node's path.extname). A name whose
// only dot is the first character has no extension.
func IsCallerScanFile(p string) bool {
	base := path.Base(p)
	if strings.LastIndexByte(base, '.') <= 0 {
		return false
	}
	switch path.Ext(base) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".go", ".java":
		return true
	}
	return false
}

// ReferenceLines returns the 1-based numbers of the lines of content that reference symbol, with
// the rules of Node's extractReferences (adapters/codeindex/extract.ts): a line is skipped when
// it is a comment line (starts with //, * or /*) or an import or re-export line; of the others
// the line, minus its // tail and its string literals, is skipped when it declares the name
// (function, class, interface, type, enum, const, let, var) and otherwise counts when it holds
// name( not preceded by a word character, $ or '.', .name(, new name or <name followed by
// whitespace, '/' or '>'. A symbol with a '.' is reduced to its last member; an empty one has no
// references. Lines are split at \n only and the JS character classes are kept as they are
// (\s includes U+00A0, U+FEFF and the Unicode spaces; \w and \b are ASCII), so the result equals
// Node's; the Go regexp package cannot express the look-behind and the back-reference of the
// original, so the matching is written out.
func ReferenceLines(content, symbol string) []int {
	name := symbol
	if i := strings.LastIndexByte(symbol, '.'); i >= 0 {
		name = symbol[i+1:]
	}
	if name == "" {
		return nil
	}
	var out []int
	for start, n := 0, 1; start <= len(content); n++ {
		end := strings.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content) - start
		}
		raw := content[start : start+end]
		start += end + 1
		if !strings.Contains(raw, name) || isCommentLine(raw) || isImportLine(raw) {
			continue
		}
		line := sanitizeLine(raw)
		if !strings.Contains(line, name) {
			continue
		}
		if anyOccurrence(line, name, declares) {
			continue
		}
		if anyOccurrence(line, name, references) {
			out = append(out, n)
		}
	}
	return out
}

// anyOccurrence reports whether pred holds for some occurrence of name in line, given the text
// before and after the occurrence. Occurrences may overlap.
func anyOccurrence(line, name string, pred func(name, before, after string) bool) bool {
	for from := 0; from < len(line); {
		i := strings.Index(line[from:], name)
		if i < 0 {
			return false
		}
		p := from + i
		if pred(name, line[:p], line[p+len(name):]) {
			return true
		}
		from = p + 1
	}
	return false
}

// declares is declRe: (function\s*\*?\s*|class\s+|interface\s+|type\s+|enum\s+|(const|let|var)\s+)name\b.
func declares(name, before, after string) bool {
	if !boundaryAfter(name, after) {
		return false
	}
	t := trimRightSpace(before)
	t = strings.TrimSuffix(t, "*")
	if strings.HasSuffix(trimRightSpace(t), "function") {
		return true
	}
	return keywordThenSpace(before, "class", "interface", "type", "enum", "const", "let", "var")
}

// declRule names the extra declaration rules of a scanned file's language (ScanFile), applied on
// top of declares. TypeScript and JavaScript files have none: they keep Node's rules exactly.
type declRule uint8

const (
	declNode declRule = iota
	declGo
	declJava
)

// declRuleFor picks the rules by the file's extension, case-sensitive like IsCallerScanFile.
func declRuleFor(file string) declRule {
	switch {
	case strings.HasSuffix(file, ".go"):
		return declGo
	case strings.HasSuffix(file, ".java"):
		return declJava
	}
	return declNode
}

// lineDecl is what the Go and Java declaration tests need from a whole (sanitized) line. It is
// computed once per line so that the test of each occurrence only looks at the text next to it
// and a line stays linear in its length however many occurrences it holds.
type lineDecl struct {
	rule declRule
	// funcEnd is, for Go, the index just past a leading func keyword (leading whitespace allowed,
	// followed by whitespace or '('), -1 without one; recv is the index of the first non-space
	// byte after it.
	funcEnd, recv int
	// firstLT is, for Java, the index of the line's first '<', -1 without one.
	firstLT int
}

func newLineDecl(rule declRule, line string) lineDecl {
	d := lineDecl{rule: rule, funcEnd: -1, recv: -1, firstLT: -1}
	switch rule {
	case declGo:
		lead := len(line) - len(trimLeftSpace(line))
		rest, ok := strings.CutPrefix(line[lead:], "func")
		if !ok || rest == "" {
			break
		}
		if r, _ := utf8.DecodeRuneInString(rest); r != '(' && !isJSSpace(r) {
			break
		}
		d.funcEnd = len(line) - len(rest)
		d.recv = len(line) - len(trimLeftSpace(rest))
	case declJava:
		d.firstLT = strings.IndexByte(line, '<')
	}
	return d
}

// declares reports whether the occurrence of name at byte p of line declares it under the
// file's language rules (always false for TypeScript and JavaScript).
func (d lineDecl) declares(line, name string, p int) bool {
	before, after := line[:p], line[p+len(name):]
	if d.rule == declNode || !startsWithParen(after) {
		return false
	}
	if d.rule == declGo {
		return d.goDeclares(before)
	}
	return d.javaDeclares(before)
}

// goDeclares: the line's code is func name( or func (receiver) name(.
func (d lineDecl) goDeclares(before string) bool {
	if d.funcEnd < 0 {
		return false
	}
	t := trimRightSpace(before)
	if len(t) == d.funcEnd { // func name(
		return len(before) > d.funcEnd
	}
	// func (receiver) name(: the receiver opens right after func and closes right before name.
	return d.recv < len(t) && before[d.recv] == '(' && t[len(t)-1] == ')'
}

// isJavaCallKeyword reports a word token a call may follow; any other word before name( is a type
// or a modifier, so the occurrence declares a method or constructor.
func isJavaCallKeyword(w string) bool {
	switch w {
	case "return", "throw", "new", "else", "case", "yield", "assert", "do":
		return true
	}
	return false
}

// javaDeclares: name( is preceded, after whitespace, by a type or modifier token (a word that is
// not a keyword a call may follow, a closing ']' of an array type, or a '>' closing a type
// argument list opened on the line) rather than '.', '=', '(', ',', "return", "->" or the line
// start.
func (d lineDecl) javaDeclares(before string) bool {
	t := trimRightSpace(before)
	if t == "" || len(t) == len(before) {
		return false
	}
	switch last := t[len(t)-1]; {
	case last == ']':
		return true
	case last == '>':
		return d.firstLT >= 0 && d.firstLT < len(t)-1 && !strings.HasSuffix(t, "->")
	case isWordASCII(rune(last)):
		i := len(t) - 1
		for i > 0 && isWordASCII(rune(t[i-1])) {
			i--
		}
		return !isJavaCallKeyword(t[i:])
	}
	return false
}

// references is callRe | memberCallRe | newRe | jsxRe.
func references(name, before, after string) bool {
	if startsWithParen(after) {
		if strings.HasSuffix(before, ".") { // .name(
			return true
		}
		if r, _ := utf8.DecodeLastRuneInString(before); before == "" || !(isWordASCII(r) || r == '$' || r == '.') { // name(
			return true
		}
	}
	if keywordThenSpace(before, "new") && boundaryAfter(name, after) { // new name
		return true
	}
	if strings.HasSuffix(before, "<") && after != "" { // <name[\s/>]
		r, _ := utf8.DecodeRuneInString(after)
		return r == '/' || r == '>' || isJSSpace(r)
	}
	return false
}

// keywordThenSpace reports whether before ends with one of kws followed by at least one
// whitespace character.
func keywordThenSpace(before string, kws ...string) bool {
	t := trimRightSpace(before)
	if len(t) == len(before) {
		return false
	}
	for _, kw := range kws {
		if strings.HasSuffix(t, kw) {
			return true
		}
	}
	return false
}

// startsWithParen reports whether s is \s*\(.
func startsWithParen(s string) bool { return strings.HasPrefix(trimLeftSpace(s), "(") }

// boundaryAfter reports whether JS \b holds between the end of name and the start of after.
func boundaryAfter(name, after string) bool {
	last, _ := utf8.DecodeLastRuneInString(name)
	next := false
	if after != "" {
		r, _ := utf8.DecodeRuneInString(after)
		next = isWordASCII(r)
	}
	return isWordASCII(last) != next
}

func isWordASCII(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
}

// isJSLineTerminator is what a JS '.' does not match (\n cannot occur: lines are split there).
func isJSLineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

func trimLeftSpace(s string) string { return strings.TrimLeftFunc(s, isJSSpace) }

func trimRightSpace(s string) string { return strings.TrimRightFunc(s, isJSSpace) }

// isCommentLine is LINE_COMMENT: ^\s*(//|\*|/\*).
func isCommentLine(raw string) bool {
	t := trimLeftSpace(raw)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// isImportLine is IMPORT_LINE: ^\s*import\s | ^\s*export\s+\{[^}]*\}\s+from\b | ^\s*export\s+\*\s+from\b.
func isImportLine(raw string) bool {
	t := trimLeftSpace(raw)
	if rest, ok := strings.CutPrefix(t, "import"); ok {
		if r, _ := utf8.DecodeRuneInString(rest); rest != "" && isJSSpace(r) {
			return true
		}
	}
	rest, ok := strings.CutPrefix(t, "export")
	if !ok {
		return false
	}
	body := trimLeftSpace(rest)
	if len(body) == len(rest) {
		return false
	}
	switch {
	case strings.HasPrefix(body, "{"):
		i := strings.IndexByte(body, '}')
		if i < 0 {
			return false
		}
		return spaceThenFrom(body[i+1:])
	case strings.HasPrefix(body, "*"):
		return spaceThenFrom(body[1:])
	}
	return false
}

// spaceThenFrom is \s+from\b at the start of s.
func spaceThenFrom(s string) bool {
	t := trimLeftSpace(s)
	if len(t) == len(s) {
		return false
	}
	rest, ok := strings.CutPrefix(t, "from")
	if !ok {
		return false
	}
	return boundaryAfter("from", rest)
}

// sanitizeLine is sanitizeLine of extract.ts: the // tail is cut, then each string literal
// ("..." '...' `...`, with backslash escapes) becomes "".
func sanitizeLine(raw string) string {
	s := raw
	// replace(/\/\/.*$/, ''): '.' stops at a line terminator and '$' only matches at the very
	// end, so a // is a comment tail only when no terminator follows it. A CRLF file therefore
	// keeps its comment tails.
	from := 0
	if strings.ContainsAny(s, "\r  ") {
		for i, r := range s {
			if isJSLineTerminator(r) {
				from = i + utf8.RuneLen(r)
			}
		}
	}
	if k := strings.Index(s[from:], "//"); k >= 0 {
		s = s[:from+k]
	}
	return blankStrings(s)
}

// blankStrings is replace(/(["'`])(?:\\.|(?!\1).)*\1/g, '""'). The pattern backtracks, so the
// end of the match from every position is computed right to left once per quote character: at
// i, first try an escape pair, then any other character that is not the quote, and finally the
// closing quote.
func blankStrings(s string) string {
	if !strings.ContainsAny(s, "\"'`") {
		return s
	}
	var ends [3][]int32
	quoteIdx := func(c byte) int {
		switch c {
		case '"':
			return 0
		case '\'':
			return 1
		}
		return 2
	}
	endsFor := func(q byte) []int32 {
		k := quoteIdx(q)
		if ends[k] != nil {
			return ends[k]
		}
		n := len(s)
		f := make([]int32, n+1)
		for i := range f {
			f[i] = -1
		}
		for i := n - 1; i >= 0; i-- {
			if !utf8.RuneStart(s[i]) {
				continue
			}
			r, sz := utf8.DecodeRuneInString(s[i:])
			if r == '\\' && i+1 < n {
				r2, sz2 := utf8.DecodeRuneInString(s[i+1:])
				if !isJSLineTerminator(r2) && f[i+1+sz2] >= 0 {
					f[i] = f[i+1+sz2]
					continue
				}
			}
			if r != rune(q) && !isJSLineTerminator(r) && f[i+sz] >= 0 {
				f[i] = f[i+sz]
				continue
			}
			if r == rune(q) {
				f[i] = int32(i + 1)
			}
		}
		ends[k] = f
		return f
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '"' || c == '\'' || c == '`' {
			if e := endsFor(c)[i+1]; e >= 0 {
				sb.WriteString(`""`)
				i = int(e)
				continue
			}
		}
		sb.WriteByte(c)
		i++
	}
	return sb.String()
}
