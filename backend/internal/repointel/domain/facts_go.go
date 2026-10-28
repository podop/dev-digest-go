package domain

import (
	"regexp"
	"strings"
)

// Go fact patterns (FR22, FR23). Every pattern is RE2, matched against one line at a time with a
// single FindStringSubmatch, so each scan is linear in the line. Verbs are upper-case literals,
// matched case-sensitively on purpose (EC9: r.Get and http.Get are not routes), so no case
// folding is involved.
const (
	goSpace = `[ \t]*`
	goStr   = `(?:"([^"]*)"|` + "`([^`]*)`)" // groups: interpreted string, raw string
)

var (
	// goHandleRe: X.HandleFunc("pattern" / X.Handle("pattern". Groups 1-2: the pattern.
	goHandleRe = regexp.MustCompile(`\.Handle(?:Func)?` + goSpace + `\(` + goSpace + goStr)

	// goVerbRe: e.GET("/path" and the other upper-case verbs. Groups 1 verb, 2-3 the path.
	goVerbRe = regexp.MustCompile(`\.(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)` + goSpace + `\(` + goSpace +
		`(?:"(/[^"]*)"|` + "`(/[^`]*)`)")

	// goAnyRe: g.Any("/path". Groups 1-2 the path.
	goAnyRe = regexp.MustCompile(`\.Any` + goSpace + `\(` + goSpace + `(?:"(/[^"]*)"|` + "`(/[^`]*)`)")

	// goGRPCRe: pb.RegisterXxxServer( with any qualifier. Group 1 Xxx.
	goGRPCRe = regexp.MustCompile(`\bRegister([A-Za-z0-9_]+)Server` + goSpace + `\(`)

	// goCronRe: c.AddFunc("spec" / c.AddJob("spec". Groups 1-2 the spec.
	goCronRe = regexp.MustCompile(`\.Add(?:Func|Job)` + goSpace + `\(` + goSpace + goStr)
)

// extractGoFacts returns the endpoints and cron specs of a Go file, de-duplicated in first-seen
// order. Neither result is nil.
func extractGoFacts(text string) (endpoints, crons []string) {
	eps, cs := newOrderedSet(), newOrderedSet()
	for line := range strings.SplitSeq(text, "\n") {
		if m := goHandleRe.FindStringSubmatch(line); m != nil {
			if ep, ok := goMuxEndpoint(m[1] + m[2]); ok {
				eps.add(ep)
			}
		}
		if m := goVerbRe.FindStringSubmatch(line); m != nil {
			eps.add(m[1] + " " + m[2] + m[3])
		}
		if m := goAnyRe.FindStringSubmatch(line); m != nil {
			eps.add("ANY " + m[1] + m[2])
		}
		if m := goGRPCRe.FindStringSubmatch(line); m != nil {
			eps.add("GRPC " + m[1])
		}
		if m := goCronRe.FindStringSubmatch(line); m != nil {
			cs.add(m[1] + m[2])
		}
	}
	return eps.list, cs.list
}

// goMuxEndpoint reads a Go 1.22 ServeMux pattern "[METHOD ][HOST]/[PATH]": the host is dropped and
// a pattern without a method is ANY. ok is false when the pattern has no path.
func goMuxEndpoint(pattern string) (string, bool) {
	method := "ANY"
	rest := strings.TrimLeft(pattern, " \t")
	if i := strings.IndexAny(rest, " \t"); i > 0 && isUpperASCII(rest[:i]) {
		method = rest[:i]
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", false
	}
	return method + " " + rest[slash:], true
}

func isUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return s != ""
}
