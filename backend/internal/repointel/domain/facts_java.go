package domain

import "strings"

// Java facts (FR24) come from an annotation scanner over the whole file text, not from line
// patterns: Spring mappings and @Scheduled spread their arguments over several lines.

// MaxAnnotationBytes caps the argument list of one annotation (the text between its
// parentheses); a longer one is skipped (EC10).
const MaxAnnotationBytes = 4096

// Annotation is one Java annotation of a file: Name as written (qualified names keep their
// dots), Args the text between its balanced parentheses ("" without), Line the 1-based line of
// its '@'.
type Annotation struct {
	Name, Args string
	Line       int
}

// JavaAnnotations returns the annotations of text in source order. Literals, text blocks and
// comments are skipped, so a ')' or '@' inside them neither ends nor starts an annotation. An
// argument list that never closes or is longer than MaxAnnotationBytes is skipped and scanning
// continues after its @Name token; an annotation inside the arguments of a reported one is part
// of those arguments only. The result is never nil.
func JavaAnnotations(text string) []Annotation {
	anns, _ := scanJava(text)
	out := make([]Annotation, len(anns))
	for i, a := range anns {
		out[i] = a.Annotation
	}
	return out
}

// javaTokKind classifies the tokens of javaLexer.
type javaTokKind uint8

const (
	jtEOF    javaTokKind = iota
	jtIdent              // identifier, keyword or number literal
	jtString             // closed "…" string literal
	jtOther              // text block, character literal or unterminated literal
	jtPunct              // any other single byte
)

type javaTok struct {
	kind       javaTokKind
	start, end int // the token is text[start:end]
	line       int // 1-based line of start
}

// javaLexer is a single forward pass over Java source. Its states are code (between tokens),
// line comment, block comment, string, character literal and text block; the last five are
// consumed whole by the call that enters them, so every byte is visited once. A string or
// character literal ends at its closing quote or before a newline (Java forbids raw newlines in
// them), which keeps an unterminated literal from swallowing the rest of the file.
type javaLexer struct {
	s    string
	i    int
	line int
	buf  javaTok
	has  bool // buf holds a peeked token
}

func newJavaLexer(s string) *javaLexer { return &javaLexer{s: s, line: 1} }

func (lx *javaLexer) next() javaTok {
	if lx.has {
		lx.has = false
		return lx.buf
	}
	return lx.lex()
}

func (lx *javaLexer) peek() javaTok {
	if !lx.has {
		lx.buf, lx.has = lx.lex(), true
	}
	return lx.buf
}

func (lx *javaLexer) text(t javaTok) string { return lx.s[t.start:t.end] }

// stringValue is the content of a jtString token as written (escapes are kept).
func (lx *javaLexer) stringValue(t javaTok) string { return lx.s[t.start+1 : t.end-1] }

func (lx *javaLexer) isPunct(t javaTok, c byte) bool {
	return t.kind == jtPunct && lx.s[t.start] == c
}

func (lx *javaLexer) lex() javaTok {
	s := lx.s
	for lx.i < len(s) {
		c := s[lx.i]
		switch {
		case c == '\n':
			lx.line++
			lx.i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			lx.i++
		case c == '/' && lx.i+1 < len(s) && s[lx.i+1] == '/':
			if j := strings.IndexByte(s[lx.i:], '\n'); j >= 0 {
				lx.i += j // the newline is counted by the loop
			} else {
				lx.i = len(s)
			}
		case c == '/' && lx.i+1 < len(s) && s[lx.i+1] == '*':
			end := len(s)
			if j := strings.Index(s[lx.i+2:], "*/"); j >= 0 {
				end = lx.i + 2 + j + 2
			}
			lx.line += strings.Count(s[lx.i:end], "\n")
			lx.i = end
		default:
			return lx.token()
		}
	}
	return javaTok{kind: jtEOF, start: len(s), end: len(s), line: lx.line}
}

// token reads the token at lx.i, which is not white space or a comment.
func (lx *javaLexer) token() javaTok {
	s, start := lx.s, lx.i
	t := javaTok{start: start, line: lx.line}
	switch c := s[start]; {
	case strings.HasPrefix(s[start:], `"""`):
		t.kind = jtOther
		lx.i = lx.textBlockEnd(start + 3)
	case c == '"' || c == '\'':
		end, closed := quotedEnd(s, start+1, c)
		lx.i = end
		t.kind = jtOther
		if closed && c == '"' {
			t.kind = jtString
		}
	case isJavaIdentByte(c):
		j := start + 1
		for j < len(s) && isJavaIdentByte(s[j]) {
			j++
		}
		lx.i = j
		t.kind = jtIdent
	default:
		lx.i = start + 1
		t.kind = jtPunct
	}
	t.end = lx.i
	return t
}

// quotedEnd returns the offset after the closing quote q of a literal whose content starts at
// i, or the offset of the newline (or the end) that leaves it unterminated.
func quotedEnd(s string, i int, q byte) (end int, closed bool) {
	for i < len(s) {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && s[i+1] != '\n' {
				i += 2
				continue
			}
		case q:
			return i + 1, true
		case '\n':
			return i, false
		}
		i++
	}
	return len(s), false
}

// textBlockEnd returns the offset after the closing """ of a text block whose content starts at
// i, or the end of the text.
func (lx *javaLexer) textBlockEnd(i int) int {
	s := lx.s
	for i < len(s) {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			if s[i+1] == '\n' {
				lx.line++
			}
			i += 2
			continue
		case s[i] == '\n':
			lx.line++
		case strings.HasPrefix(s[i:], `"""`):
			return i + 3
		}
		i++
	}
	return len(s)
}

// isJavaIdentByte accepts ASCII identifier characters and every byte of a non-ASCII UTF-8
// sequence (Java identifiers may hold Unicode letters).
func isJavaIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

// javaAnn is a scanned annotation with its span text[at:end] ('@' to after ')' or the name).
type javaAnn struct {
	Annotation
	at, end int
	ok      bool // the name has no argument list, or the list closed within the cap
}

// scanJava runs the lexer once over text and returns the reported annotations plus declAt, the
// offset of the first class/interface keyword outside comments and literals (-1 when none).
//
// Argument lists are matched with one parenthesis depth counter for the whole file and a stack
// of the annotations whose list is open: the ')' that brings the depth back to the level of the
// innermost open annotation closes it. This is exactly the balanced-paren capture from each
// annotation's '(', but no byte is scanned twice, so a skipped (unclosed or too long) list costs
// nothing extra; the annotations inside it were seen by the same pass, and dropping those that
// lie inside a reported annotation's span reproduces "resume after the @Name token".
func scanJava(text string) (anns []javaAnn, declAt int) {
	type open struct{ idx, depth, paren int }
	lx := newJavaLexer(text)
	var all []javaAnn
	var stack []open
	depth := 0
	declAt = -1
	prevDot := false
	markDecl := func(at int) {
		if declAt < 0 {
			declAt = at
		}
	}
	for t := lx.next(); t.kind != jtEOF; t = lx.next() {
		dot := false
		switch {
		case t.kind == jtIdent:
			if w := lx.text(t); (w == "class" || w == "interface") && !prevDot {
				markDecl(t.start)
			}
		case lx.isPunct(t, '@'):
			if n := lx.peek(); n.kind != jtIdent {
				break
			} else if lx.text(n) == "interface" { // @interface declares an annotation type
				lx.next()
				markDecl(t.start)
				break
			}
			a, dotted := lx.annotationName(t)
			dot = dotted
			if p := lx.peek(); lx.isPunct(p, '(') {
				lx.next()
				stack = append(stack, open{idx: len(all), depth: depth, paren: p.start})
				depth++
			} else {
				a.ok = true
			}
			all = append(all, a)
		case lx.isPunct(t, '('):
			depth++
		case lx.isPunct(t, ')'):
			if depth == 0 {
				break
			}
			depth--
			if n := len(stack); n > 0 && stack[n-1].depth == depth {
				o := stack[n-1]
				stack = stack[:n-1]
				if t.start-o.paren-1 <= MaxAnnotationBytes {
					a := &all[o.idx]
					a.Args, a.end, a.ok = text[o.paren+1:t.start], t.end, true
				}
			}
		case lx.isPunct(t, '.'):
			dot = true
		}
		prevDot = dot
	}
	anns = make([]javaAnn, 0, len(all))
	cover := 0 // end of the last reported annotation; spans nest, so one bound suffices
	for _, a := range all {
		if a.ok && a.at >= cover {
			anns = append(anns, a)
			cover = a.end
		}
	}
	return anns, declAt
}

// annotationName reads the (possibly qualified) name after the '@' token at; the next token is
// known to be an identifier. dotted reports a trailing '.' consumed without a following name.
func (lx *javaLexer) annotationName(at javaTok) (a javaAnn, dotted bool) {
	id := lx.next()
	var name strings.Builder // a Builder keeps a name of many segments linear
	name.WriteString(lx.text(id))
	end := id.end
	for lx.isPunct(lx.peek(), '.') {
		lx.next()
		n := lx.peek()
		if n.kind != jtIdent {
			dotted = true
			break
		}
		lx.next()
		name.WriteString(".")
		name.WriteString(lx.text(n))
		end = n.end
	}
	return javaAnn{Annotation: Annotation{Name: name.String(), Line: at.line}, at: at.start, end: end}, dotted
}

// javaVerbMappings maps the shortcut mapping annotations to their HTTP method.
var javaVerbMappings = map[string]string{
	"GetMapping": "GET", "PostMapping": "POST", "PutMapping": "PUT", "DeleteMapping": "DELETE", "PatchMapping": "PATCH",
}

// javaRequestMethods are the constants of Spring's RequestMethod.
var javaRequestMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true, "TRACE": true,
}

// javaEndpointOutputFactor bounds the total bytes of a Java file's endpoints to this many times
// the file's length. Every endpoint repeats the class prefix (up to MaxAnnotationBytes), so a
// hostile clone with a 4 KiB prefix and thousands of tiny mappings would otherwise turn 400 KiB
// of text into ~80 MB of endpoint strings, an output quadratic in the input (NFR3). Real
// controllers stay far below it: an endpoint is shorter than the annotation and method that
// declare it.
const javaEndpointOutputFactor = 4

// javaEndpointKey identifies an endpoint before its string is built: within one file the prefix
// is fixed and joinPath(prefix, p) depends only on TrimLeft(p, "/"), so equal keys are equal
// endpoints and a duplicate costs no join.
type javaEndpointKey struct{ method, path string }

// extractJavaFacts returns the Spring endpoints and @Scheduled crons of a Java file,
// de-duplicated in first-seen order. Endpoints stop at the first one that would take their total
// length past javaEndpointOutputFactor × len(text); those before it are kept. Neither result is
// nil.
func extractJavaFacts(text string) (endpoints, crons []string) {
	endpoints, cs := []string{}, newOrderedSet()
	seen := map[javaEndpointKey]struct{}{}
	budget, full := javaEndpointOutputFactor*len(text), false
	anns, declAt := scanJava(text)

	prefixIdx, prefix, prefixKnown := -1, "", true
	for i, a := range anns {
		if declAt >= 0 && a.at < declAt && simpleName(a.Name) == "RequestMapping" {
			prefixIdx = i
			// Only the first path is the prefix; a non-literal one leaves every mapping unknown.
			first := javaPaths(javaArgs(a.Args))[0]
			prefix, prefixKnown = first.value, first.ok
			break
		}
	}

	for i, a := range anns {
		name := simpleName(a.Name)
		if name == "Scheduled" {
			for _, c := range javaSchedules(javaArgs(a.Args)) {
				cs.add(c)
			}
			continue
		}
		verb, isVerb := javaVerbMappings[name]
		if full || i == prefixIdx || !prefixKnown || (!isVerb && name != "RequestMapping") {
			continue
		}
		args := javaArgs(a.Args)
		methods := []string{verb}
		if !isVerb {
			methods = javaMethods(args)
		}
	paths:
		for _, p := range javaPaths(args) {
			if !p.ok {
				continue
			}
			for _, m := range methods {
				k := javaEndpointKey{method: m, path: strings.TrimLeft(p.value, "/")}
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
				ep := m + " " + joinPath(prefix, p.value)
				if len(ep) > budget {
					full = true
					break paths
				}
				budget -= len(ep)
				endpoints = append(endpoints, ep)
			}
		}
	}
	return endpoints, cs.list
}

func simpleName(name string) string {
	return name[strings.LastIndexByte(name, '.')+1:]
}

// joinPath joins a class prefix and a method path with exactly one '/' between them; the result
// always starts with '/', and an empty join is "/".
func joinPath(prefix, path string) string {
	r := strings.TrimRight(prefix, "/")
	if p := strings.TrimLeft(path, "/"); p != "" {
		r += "/" + p
	}
	if !strings.HasPrefix(r, "/") {
		r = "/" + r
	}
	return r
}

// javaArg is one top-level element of an argument list: name "value" for the unnamed one.
type javaArg struct {
	name string
	val  []javaTok
	lx   *javaLexer
}

// javaArgs splits args (the captured text of one annotation, ≤ MaxAnnotationBytes) into its
// top-level elements. The lexer runs over args from a token boundary in code, so it yields the
// same tokens as the file scan did.
func javaArgs(args string) []javaArg {
	lx := newJavaLexer(args)
	var toks []javaTok
	for t := lx.next(); t.kind != jtEOF; t = lx.next() {
		toks = append(toks, t)
	}
	var out []javaArg
	for _, el := range splitTopLevel(lx, toks) {
		if len(el) >= 2 && el[0].kind == jtIdent && lx.isPunct(el[1], '=') {
			out = append(out, javaArg{name: lx.text(el[0]), val: el[2:], lx: lx})
		} else if len(el) > 0 {
			out = append(out, javaArg{name: "value", val: el, lx: lx})
		}
	}
	return out
}

// splitTopLevel splits toks at the commas outside (), {} and []; an empty last element (a
// trailing comma) is dropped, other empty elements are kept.
func splitTopLevel(lx *javaLexer, toks []javaTok) [][]javaTok {
	var out [][]javaTok
	depth, start := 0, 0
	for i, t := range toks {
		if t.kind != jtPunct {
			continue
		}
		switch lx.s[t.start] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// elements returns the items of a {…} array value, or the value itself as one item.
func (a javaArg) elements() [][]javaTok {
	v := a.val
	if len(v) >= 2 && a.lx.isPunct(v[0], '{') && a.lx.isPunct(v[len(v)-1], '}') {
		return splitTopLevel(a.lx, v[1:len(v)-1])
	}
	return [][]javaTok{v}
}

// literal returns the content of an element that is exactly one string literal.
func (a javaArg) literal(el []javaTok) (string, bool) {
	if len(el) == 1 && el[0].kind == jtString {
		return a.lx.stringValue(el[0]), true
	}
	return "", false
}

type javaPath struct {
	value string
	ok    bool // false: the element is not a string literal and its path is skipped
}

// javaPaths returns the paths of value/path; no such argument, or an empty array, is the empty
// path. The result has at least one element.
func javaPaths(args []javaArg) []javaPath {
	var out []javaPath
	found := false
	for _, a := range args {
		if a.name != "value" && a.name != "path" {
			continue
		}
		found = true
		els := a.elements()
		if len(els) == 0 {
			out = append(out, javaPath{ok: true})
		}
		for _, el := range els {
			v, ok := a.literal(el)
			out = append(out, javaPath{value: v, ok: ok})
		}
	}
	if !found {
		return []javaPath{{ok: true}}
	}
	return out
}

// javaMethods returns the methods of a @RequestMapping: ANY without method (or with an empty
// array), else every RequestMethod constant named as X or RequestMethod.X; other elements are
// skipped. The result is de-duplicated (at most nine methods), so paths × methods stays linear
// in the argument text.
func javaMethods(args []javaArg) []string {
	out := newOrderedSet()
	found := false
	for _, a := range args {
		if a.name != "method" {
			continue
		}
		found = true
		els := a.elements()
		if len(els) == 0 {
			out.add("ANY")
		}
		for _, el := range els {
			if m, ok := a.constantName(el); ok && javaRequestMethods[m] {
				out.add(m)
			}
		}
	}
	if !found {
		return []string{"ANY"}
	}
	return out.list
}

// constantName returns the last identifier of a name X or A.B.X.
func (a javaArg) constantName(el []javaTok) (string, bool) {
	if len(el)%2 == 0 {
		return "", false
	}
	for i, t := range el {
		if i%2 == 0 && t.kind != jtIdent || i%2 == 1 && !a.lx.isPunct(t, '.') {
			return "", false
		}
	}
	return a.lx.text(el[len(el)-1]), true
}

// javaSchedules returns the crons of one @Scheduled: the cron string as written, and
// "<attribute>=<value>" for fixedRate/fixedDelay(String) given as a string or a number literal.
func javaSchedules(args []javaArg) []string {
	var out []string
	for _, a := range args {
		switch a.name {
		case "cron":
			if v, ok := a.literal(a.val); ok && v != "" {
				out = append(out, v)
			}
		case "fixedRate", "fixedDelay", "fixedRateString", "fixedDelayString":
			if v, ok := a.literal(a.val); ok && v != "" {
				out = append(out, a.name+"="+v)
			} else if len(a.val) == 1 && a.val[0].kind == jtIdent && isDigit(a.lx.s[a.val[0].start]) {
				out = append(out, a.name+"="+a.lx.text(a.val[0]))
			}
		}
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
