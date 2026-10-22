package domain

import (
	"context"
	"regexp"
	"strings"
)

// ExtractedSymbol is a symbol the regex extractor found: the clone-scan fallback of BlastRadius
// has no parser, it uses the line-based extractor of Node's adapters/codeindex/extract.ts.
type ExtractedSymbol struct {
	Name string
	// Kind is function, class, method, interface, type or enum.
	Kind string
	// Line is 1-based.
	Line int
}

// The six declaration patterns and the class-body method pattern of extractSymbols, ported from
// JavaScript's dialect to RE2 like the fact patterns of facts.go (\s is written out as
// ECMAScript's white space set, \w as its ASCII class). None of them needs a look-behind or a
// back-reference, so the Go regexp package, which both finds the leftmost match a backtracking
// engine would find first and runs in time linear in the line, is the right tool: a hostile
// two-megabyte line cannot make it backtrack.
const (
	symIdent = `([A-Za-z_$][0-9A-Za-z_$]*)`
	symOpt   = `(?:export` + jsSpace + `+)?`
)

type symbolPattern struct {
	re   *regexp.Regexp
	kind string
	// words: the line must hold one of them for the pattern to match (a literal of the pattern),
	// checked before running the expression.
	words []string
}

var (
	symbolPatterns = []symbolPattern{
		{kind: "function", words: []string{"function"}, re: regexp.MustCompile(
			symOpt + `(?:default` + jsSpace + `+)?(?:async` + jsSpace + `+)?function` + jsSpace + `*\*?` + jsSpace + `*` +
				symIdent + jsSpace + `*[(<]`)},
		{kind: "class", words: []string{"class"}, re: regexp.MustCompile(
			symOpt + `(?:default` + jsSpace + `+)?(?:abstract` + jsSpace + `+)?class` + jsSpace + `+` + symIdent)},
		{kind: "function", words: []string{"const", "let", "var"}, re: regexp.MustCompile(
			symOpt + `(?:const|let|var)` + jsSpace + `+` + symIdent + jsSpace + `*(?::[^=]+)?=` + jsSpace + `*(?:async` + jsSpace + `*)?` +
				`(?:function\b|\([^)]*\)` + jsSpace + `*(?::[^=]*)?=>|[A-Za-z_$][0-9A-Za-z_$]*` + jsSpace + `*=>)`)},
		{kind: "interface", words: []string{"interface"}, re: regexp.MustCompile(symOpt + `interface` + jsSpace + `+` + symIdent)},
		{kind: "type", words: []string{"type"}, re: regexp.MustCompile(symOpt + `type` + jsSpace + `+` + symIdent + jsSpace + `*[=<]`)},
		{kind: "enum", words: []string{"enum"}, re: regexp.MustCompile(symOpt + `enum` + jsSpace + `+` + symIdent)},
	}

	// methodRe: name(args) {  |  async name(args) {  |  static name(args) { ...
	methodRe = regexp.MustCompile(`^` + jsSpace + `*(?:public` + jsSpace + `+|private` + jsSpace + `+|protected` + jsSpace + `+|static` +
		jsSpace + `+|readonly` + jsSpace + `+|async` + jsSpace + `+|\*` + jsSpace + `*)*` + symIdent + jsSpace + `*\([^)]*\)` + jsSpace +
		`*(?::[^={]+)?\{`)

	// extractKeywords are names the extractor never treats as a symbol.
	extractKeywords = map[string]struct{}{
		"if": {}, "for": {}, "while": {}, "switch": {}, "catch": {}, "return": {}, "function": {}, "await": {},
		"typeof": {}, "new": {}, "delete": {}, "void": {}, "do": {}, "else": {}, "in": {}, "of": {},
		"instanceof": {}, "yield": {}, "super": {}, "constructor": {}, "get": {}, "set": {}, "import": {},
		"export": {}, "as": {}, "from": {}, "class": {}, "extends": {},
	}
)

// ExtractSymbols is extractSymbols of Node's adapters/codeindex/extract.ts: a line-based scan
// that skips comment lines (their braces still count), finds a declaration with the six patterns
// in order (the first whose name is not a keyword wins), reports the methods one level inside a
// class as "<Class>.<method>" and "<method>", and drops repeats of (name, kind, line). Lines are
// split at \n only, as in Node. Symbols come in source order.
//
// The scan looks at ctx every few thousand units of work (a line costs its length, each pattern
// run on a line costs it again), so a hostile file cannot hold it past a deadline for long; the
// error is ctx's.
func ExtractSymbols(ctx context.Context, content string) ([]ExtractedSymbol, error) {
	var (
		out          []ExtractedSymbol
		seen         = map[extractedKey]struct{}{}
		classDepth   int
		braceDepth   int
		currentClass string
		inClass      bool
		work         int
	)
	tick := func(units int) error {
		work += units
		if work < scanTickUnits {
			return nil
		}
		work = 0
		return ctx.Err()
	}
	add := func(name, kind string, line int) {
		k := extractedKey{name, kind, line}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		out = append(out, ExtractedSymbol{Name: name, Kind: kind, Line: line})
	}

	for start, n := 0, 1; start <= len(content); n++ {
		end := strings.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content) - start
		}
		raw := content[start : start+end]
		start += end + 1
		if err := tick(len(raw) + 1); err != nil {
			return nil, err
		}
		if isCommentLine(raw) {
			braceDepth += countBraces(raw)
			continue
		}
		line := sanitizeLine(raw)
		if err := tick(len(line)); err != nil {
			return nil, err
		}

		matched := false
		for _, p := range symbolPatterns {
			if !containsAny(line, p.words) {
				continue
			}
			m := p.re.FindStringSubmatch(line)
			if err := tick(len(line)); err != nil {
				return nil, err
			}
			if m == nil {
				continue
			}
			if _, kw := extractKeywords[m[1]]; kw {
				continue
			}
			add(m[1], p.kind, n)
			if p.kind == "class" {
				currentClass, inClass, classDepth = m[1], true, braceDepth
			}
			matched = true
			break
		}

		if !matched && inClass && braceDepth == classDepth+1 && strings.IndexByte(line, '(') >= 0 && strings.IndexByte(line, '{') >= 0 {
			m := methodRe.FindStringSubmatch(line)
			if err := tick(len(line)); err != nil {
				return nil, err
			}
			if m != nil {
				if _, kw := extractKeywords[m[1]]; !kw {
					add(currentClass+"."+m[1], "method", n)
					add(m[1], "method", n)
				}
			}
		}

		braceDepth += countBraces(line)
		if inClass && braceDepth <= classDepth {
			inClass = false
		}
	}
	return out, nil
}

type extractedKey struct {
	name, kind string
	line       int
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// countBraces is the number of '{' minus the number of '}' in s.
func countBraces(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			n++
		case '}':
			n--
		}
	}
	return n
}
