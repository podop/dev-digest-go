package domain

import (
	"regexp"
	"strings"
)

// The fact patterns port Node's extractEndpoints and extractCrons (server/src/adapters/codeindex/
// extract.ts) from JavaScript's regular expression dialect to RE2 (P5):
//   - RE2 has no back-reference, so the closing quote of a route path is matched by three
//     alternatives, one per quote;
//   - \s is written out as ECMAScript's white space set (RE2's \s is ASCII only);
//   - the /i flag is written out per letter, because RE2's case folding also equates 's' with
//     U+017F and 'k' with U+212A, which JavaScript's does not.
const (
	jsSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
	anyQuot = "['\"`]"
	noQuot  = "[^'\"`]"
)

// ci spells a literal of ASCII letters, digits and underscores case-insensitively.
func ci(lit string) string {
	var sb strings.Builder
	for _, r := range lit {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			sb.WriteString("[" + strings.ToLower(string(r)) + strings.ToUpper(string(r)) + "]")
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// alt is the case-insensitive alternation of literals, as a non-capturing group.
func alt(lits ...string) string {
	parts := make([]string, len(lits))
	for i, l := range lits {
		parts[i] = ci(l)
	}
	return "(?:" + strings.Join(parts, "|") + ")"
}

var (
	// verbRe: app.get<T>('/x'. Groups: 1 verb, 2-4 the path by quote kind.
	verbRe = regexp.MustCompile(`\b` + alt("app", "router", "fastify", "server", "api") + `\.` +
		`(` + alt("get", "post", "put", "patch", "delete", "options", "head") + `)` +
		jsSpace + `*(?:<[^>]*>)?` + jsSpace + `*\(` + jsSpace + `*` +
		`(?:'(` + noQuot + `+)'|"(` + noQuot + `+)"|` + "`(" + noQuot + "+)`)")

	// routeObjRe: method: 'GET' ... url: '/x' on one line. Groups: 1 method, 2 url.
	routeObjRe = regexp.MustCompile(ci("method") + jsSpace + `*:` + jsSpace + `*` + anyQuot +
		`(` + alt("GET", "POST", "PUT", "PATCH", "DELETE") + `)` + anyQuot +
		`(?s:.*?)` + ci("url") + jsSpace + `*:` + jsSpace + `*` + anyQuot +
		`(` + noQuot + `+)` + anyQuot)

	// cronExprRe: schedule('*/5 * * * *'). Group 1 the expression.
	cronExprRe = regexp.MustCompile(`\b` + alt("cron", "schedule", "CronJob") +
		jsSpace + `*[.(]?` + jsSpace + `*(?:\()?` + jsSpace + `*` + anyQuot +
		`(` + noQuot + `*(?:\*|[0-9]+` + jsSpace + `+[0-9]+)` + noQuot + `*)` + anyQuot)

	// jobKindRe: jobs.register('kind') / enqueue(ws, 'kind'). Group 1 the kind.
	jobKindRe = regexp.MustCompile(`\b` + alt("register", "enqueue") + jsSpace + `*\(` + jsSpace + `*` +
		`(?:[A-Za-z0-9_$.]+` + jsSpace + `*,` + jsSpace + `*)?` + anyQuot +
		`([a-zA-Z][a-zA-Z0-9_]*)` + anyQuot)

	// jobWordRe: the line must also mention one of these words.
	jobWordRe = regexp.MustCompile(alt("poll", "index", "clone", "digest", "cron", "sync", "schedule", "job"))
)

// ExtractEndpoints returns the HTTP routes registered in text as "METHOD /path", one pattern
// match per line and kind, de-duplicated in first-seen order (FR3022). The result is never nil.
func ExtractEndpoints(text string) []string {
	out := newOrderedSet()
	for line := range strings.SplitSeq(text, "\n") {
		if m := verbRe.FindStringSubmatch(line); m != nil {
			p := m[2] + m[3] + m[4] // exactly one group took part
			out.add(strings.ToUpper(m[1]) + " " + p)
		}
		if m := routeObjRe.FindStringSubmatch(line); m != nil {
			out.add(strings.ToUpper(m[1]) + " " + m[2])
		}
	}
	return out.list
}

// ExtractCrons returns the cron expressions and background job kinds ("job:<kind>") of text,
// de-duplicated in first-seen order (FR3022). The result is never nil.
func ExtractCrons(text string) []string {
	out := newOrderedSet()
	for line := range strings.SplitSeq(text, "\n") {
		if m := cronExprRe.FindStringSubmatch(line); m != nil {
			out.add(strings.TrimFunc(m[1], isJSSpace))
		}
		if m := jobKindRe.FindStringSubmatch(line); m != nil && jobWordRe.MatchString(line) {
			out.add("job:" + m[1])
		}
	}
	return out.list
}

// ExtractFacts returns the facts row of one file; ok is false when the file has neither an
// endpoint nor a cron (such a file gets no row).
func ExtractFacts(path, text string) (facts FileFacts, ok bool) {
	facts = FileFacts{Path: path, Endpoints: ExtractEndpoints(text), Crons: ExtractCrons(text)}
	return facts, len(facts.Endpoints) > 0 || len(facts.Crons) > 0
}

// isJSSpace reports ECMAScript white space or line terminator, the set String.prototype.trim
// removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

type orderedSet struct {
	seen map[string]struct{}
	list []string
}

func newOrderedSet() *orderedSet {
	return &orderedSet{seen: map[string]struct{}{}, list: []string{}}
}

func (s *orderedSet) add(v string) {
	if _, dup := s.seen[v]; dup {
		return
	}
	s.seen[v] = struct{}{}
	s.list = append(s.list, v)
}
