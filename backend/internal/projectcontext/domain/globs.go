package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// GlobSet is a compiled list of document globs. Syntax: "**/" matches zero or
// more directories, "**" elsewhere matches anything, "*" any run without "/",
// "?" one character other than "/", "{a,b}" is an alternation (not nested).
// Matching is case-sensitive and anchored; dotfiles match. Everything else is literal.
type GlobSet struct {
	globs []string
	res   []*regexp.Regexp
}

// NewGlobSet compiles the globs.
func NewGlobSet(globs []string) (*GlobSet, error) {
	gs := &GlobSet{globs: append([]string(nil), globs...)}
	for _, g := range globs {
		re, err := compileGlob(g)
		if err != nil {
			return nil, fmt.Errorf("compile glob %q: %w", g, err)
		}
		gs.res = append(gs.res, re)
	}
	return gs, nil
}

// Globs returns the source globs in order.
func (g *GlobSet) Globs() []string { return append([]string(nil), g.globs...) }

// Match reports whether the path matches at least one glob. An empty set matches nothing.
func (g *GlobSet) Match(path string) bool {
	for _, re := range g.res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// anyChar is JavaScript's "." (no line terminators); RE2's "." differs on U+2028/9 and \r.
const anyChar = `[^\n\r\x{2028}\x{2029}]`

func compileGlob(glob string) (*regexp.Regexp, error) {
	var re strings.Builder
	inBraces := false
	for i := 0; i < len(glob); i++ {
		ch := glob[i]
		switch {
		case ch == '*':
			switch {
			case strings.HasPrefix(glob[i:], "**/"):
				re.WriteString(`(?:[^/]+/)*`)
				i += 2
			case strings.HasPrefix(glob[i:], "**"):
				re.WriteString(anyChar + "*")
				i++
			default:
				re.WriteString(`[^/]*`)
			}
		case ch == '?':
			re.WriteString(`[^/]`)
		case ch == '{' && !inBraces && strings.IndexByte(glob[i:], '}') > 0:
			inBraces = true
			re.WriteString(`(?:`)
		case ch == '}' && inBraces:
			inBraces = false
			re.WriteString(`)`)
		case ch == ',' && inBraces:
			re.WriteString(`|`)
		default:
			// A whole rune at a time so multi-byte characters stay intact.
			_, size := utf8.DecodeRuneInString(glob[i:])
			re.WriteString(regexp.QuoteMeta(glob[i : i+size]))
			i += size - 1
		}
	}
	return regexp.Compile(`^(?:` + re.String() + `)$`)
}
