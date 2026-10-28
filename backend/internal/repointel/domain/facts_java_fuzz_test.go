package domain

import (
	"strings"
	"testing"
)

// javaStringLiterals collects the contents of every closed "…" literal of text with the
// scanner's own lexer.
func javaStringLiterals(text string) []string {
	lx := newJavaLexer(text)
	var out []string
	for t := lx.next(); t.kind != jtEOF; t = lx.next() {
		if t.kind == jtString {
			out = append(out, lx.stringValue(t))
		}
	}
	return out
}

var javaFixedAttrs = map[string]bool{"fixedRate": true, "fixedDelay": true, "fixedRateString": true, "fixedDelayString": true}

var javaEndpointMethods = map[string]bool{
	"ANY": true, "GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true, "TRACE": true,
}

// FuzzJavaFacts (AC18): no panic; every annotation is bounded and taken from the input; every
// endpoint path is one of the input's string literals (or the empty path) joined to the class
// prefix, itself a literal of the input or empty; every cron is a literal of the input.
func FuzzJavaFacts(f *testing.F) {
	for _, tc := range springCases {
		f.Add(tc.in)
	}
	for _, seed := range []string{
		`@A(")") @B`, `@A('(') @B`, `@A("@x")`, "@A(\"\"\"\n a)b\n\"\"\") @B", "// @Fake(\n@B", "/* @Fake( */ @B",
		"@A(\"x\"\n@B(\"y\")", "@A(" + strings.Repeat("(", 50), "@RequestMapping(\"/\")\n@interface X {}",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		lines := strings.Count(in, "\n") + 1
		for _, a := range JavaAnnotations(in) {
			if a.Name == "" || len(a.Args) > MaxAnnotationBytes || !strings.Contains(in, a.Args) || a.Line < 1 || a.Line > lines {
				t.Fatalf("JavaAnnotations(%q) returned %+v: empty name, args over the cap or not from the input, or bad line", in, a)
			}
		}

		lits := javaStringLiterals(in)
		isLit := map[string]bool{}
		trimmed := map[string]bool{}
		for _, l := range append(lits, "") {
			if l != "" && !strings.Contains(in, `"`+l+`"`) {
				t.Fatalf("lexer literal %q is not a quoted string of %q", l, in)
			}
			isLit[l] = true
			trimmed[strings.TrimLeft(l, "/")] = true
		}
		fromLiterals := func(path string) bool {
			for a := range isLit {
				if joinPath(a, "") == path {
					return true
				}
				// joinPath(a, b) is head + TrimLeft(b, "/") for every b with a non-empty trim.
				head := strings.TrimSuffix(joinPath(a, "\x00"), "\x00")
				if rest, ok := strings.CutPrefix(path, head); ok && rest != "" && trimmed[rest] {
					return true
				}
			}
			return false
		}

		facts, ok := ExtractFacts("Src.java", in)
		if ok != (len(facts.Endpoints)+len(facts.Crons) > 0) {
			t.Fatalf("ExtractFacts(%q) ok = %v with %+v", in, ok, facts)
		}
		total := 0
		for _, ep := range facts.Endpoints {
			total += len(ep)
		}
		if total > javaEndpointOutputFactor*len(in) {
			t.Fatalf("ExtractFacts(%q) endpoint bytes = %d, want <= %d × %d (NFR3)", in, total, javaEndpointOutputFactor, len(in))
		}
		for _, ep := range facts.Endpoints {
			method, path, found := strings.Cut(ep, " ")
			if !found || !javaEndpointMethods[method] || !fromLiterals(path) {
				t.Fatalf("ExtractFacts(%q) endpoint %q is not METHOD join(prefix literal, literal)", in, ep)
			}
		}
		for _, c := range facts.Crons {
			if isLit[c] && c != "" {
				continue
			}
			attr, v, found := strings.Cut(c, "=")
			if !found || !javaFixedAttrs[attr] || !(isLit[v] || (v != "" && v[0] >= '0' && v[0] <= '9' && strings.Contains(in, v))) {
				t.Fatalf("ExtractFacts(%q) cron %q is neither a literal nor attribute=literal/number of the input", in, c)
			}
		}
	})
}
