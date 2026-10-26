// Package wfrules is checkWorkflow of Export to CI (spec 7.6.6): the size limit, the YAML syntax
// and the seven security rules every workflow must pass before it is shown as installable or
// published, whether the studio generated it or the user edited it in the Preview step.
//
// It is a port of the Node domain/workflow-rules.ts and application/workflow-check.ts. The text is
// read as untrusted: Check parses it into a plain value tree (load.go) with the limits of the Node
// "yaml" package (one document, no duplicate keys, alias expansion capped at 100) and walks that
// tree, so a rule never matches on text a YAML parser would not see as a value. The package
// imports only the standard library and gopkg.in/yaml.v3.
package wfrules

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Rule is the id of a broken workflow rule: the details.rule of a 422 workflow_insecure.
type Rule string

// The rule ids, in the order the rules run.
const (
	InvalidYAML         Rule = "invalid_yaml"
	ForbiddenTrigger    Rule = "forbidden_trigger"
	Permissions         Rule = "permissions"
	Secrets             Rule = "secrets"
	UnpinnedAction      Rule = "unpinned_action"
	ForkGuard           Rule = "fork_guard"
	UntrustedExpression Rule = "untrusted_expression"
)

// Result is the outcome of Check: OK, or the first broken rule.
type Result struct {
	OK   bool
	Rule Rule
}

// The limits and names the rules compare against. They equal domain.MaxWorkflowBytes,
// domain.Triggers() and the guard of the generated workflow; rules_test.go pins the first two.
// This package does not import the domain package so that its tests can.
const (
	maxWorkflowBytes = 65_536
	forkGuardExpr    = "github.event.pull_request.head.repo.full_name == github.repository"
)

var (
	allowedTriggers = []string{"opened", "synchronize", "reopened"}
	// builtinSecretNames is what Check allows next to GITHUB_TOKEN: the secret of the openrouter
	// workflow, the only one before custom providers.
	builtinSecretNames = []string{"OPENROUTER_API_KEY"}
)

// githubToken is the one secret every generated workflow may reference (FR28).
const githubToken = "GITHUB_TOKEN"

// jsSpace is the character set of JavaScript's \s and String.prototype.trim. Go's \s is ASCII only.
const jsSpace = `\t\n\x0b\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	// The owner never starts with '.', so ./x and ../x local actions fail. RE2 has no lookahead:
	// the repository part must not be "." or ".." (checked in pinned); it is the whole run of
	// repository characters up to the next '/' or '@'.
	reAction     = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/([A-Za-z0-9_.-]+)(?:/[A-Za-z0-9_./-]+)?@[0-9a-fA-F]{40}$`)
	reExpression = regexp.MustCompile(`(?s)\$\{\{(.*?)\}\}`)
	// "secrets" is case-insensitive, spelled out so that U+017F (long s) does not fold into it.
	reSecretRef = regexp.MustCompile(`\b[sS][eE][cC][rR][eE][tT][sS]\b(?:\.([A-Za-z0-9_]+))?`)
	reBracket   = regexp.MustCompile(`\[[` + jsSpace + `]*['"]?([A-Za-z0-9_-]+)['"]?[` + jsSpace + `]*\]`)
	reSpaces    = regexp.MustCompile(`[` + jsSpace + `]+`)
	reOuterExpr = regexp.MustCompile(`(?s)^\$\{\{(.*)\}\}$`)
)

// untrusted lists the expression fragments an attacker controls, lower-case.
var untrusted = []string{
	"github.event.pull_request.title",
	"github.event.pull_request.body",
	"github.event.pull_request.head.ref",
	"github.event.pull_request.head.label",
	"github.head_ref",
	"github.event.comment",
	// Whole-object dumps carry the same attacker-controlled fields.
	"tojson(github)",
	"tojson(github.event",
}

// Check runs the size limit, the YAML syntax and the seven rules over a workflow text and returns
// the first broken one. The size (UTF-8 bytes) is checked before the text is parsed. It allows
// GITHUB_TOKEN and OPENROUTER_API_KEY as secret references: CheckWith(text, []string{"OPENROUTER_API_KEY"}).
func Check(text string) Result {
	return CheckWith(text, builtinSecretNames)
}

// CheckWith is Check with the secret names the workflow of this export may reference (FR28): the
// `secrets` rule allows GITHUB_TOKEN, which is always allowed, plus names. Names are matched
// exactly (case-sensitive, as GitHub secret names are written in the generated workflow); an
// empty name allows nothing, so `secrets` without a member never passes. A name the caller does
// not list, and `secrets: inherit`, stay refused. Every other rule is the same as in Check.
func CheckWith(text string, names []string) Result {
	if len(text) > maxWorkflowBytes {
		return fail(InvalidYAML)
	}
	root, err := parse(text)
	if err != nil {
		return fail(InvalidYAML)
	}
	doc, ok := root.(*object)
	if !ok {
		return fail(InvalidYAML)
	}
	jobs, ok := jobsOf(doc)
	if !ok {
		return fail(InvalidYAML)
	}

	if !triggerOK(doc.vals["on"]) {
		return fail(ForbiddenTrigger)
	}
	if !permissionsOK(doc, jobs) {
		return fail(Permissions)
	}

	w := &walker{}
	exprs := w.expressions(doc)
	if w.exhausted {
		return fail(InvalidYAML)
	}
	if !secretsOK(jobs, exprs, names) {
		return fail(Secrets)
	}
	if !actionsPinned(jobs) {
		return fail(UnpinnedAction)
	}
	if !forkGuarded(jobs) {
		return fail(ForkGuard)
	}
	for _, e := range exprs {
		n := normalize(e)
		if slices.ContainsFunc(untrusted, func(u string) bool { return strings.Contains(n, u) }) {
			return fail(UntrustedExpression)
		}
	}
	return Result{OK: true}
}

func fail(r Rule) Result { return Result{Rule: r} }

// jobsOf returns the jobs when `jobs` is a non-empty mapping of mappings.
func jobsOf(doc *object) ([]*object, bool) {
	m, ok := doc.vals["jobs"].(*object)
	if !ok || len(m.keys) == 0 {
		return nil, false
	}
	jobs := make([]*object, 0, len(m.keys))
	for _, k := range m.keys {
		j, ok := m.vals[k].(*object)
		if !ok {
			return nil, false
		}
		jobs = append(jobs, j)
	}
	return jobs, true
}

// triggerOK: `on` is a mapping with exactly the key pull_request, whose value is null or a mapping
// whose optional `types` is a string or a list of allowed triggers.
func triggerOK(on value) bool {
	m, ok := on.(*object)
	if !ok || len(m.keys) != 1 {
		return false
	}
	pr, ok := m.vals["pull_request"]
	if !ok {
		return false
	}
	if pr == nil {
		return true
	}
	prMap, ok := pr.(*object)
	if !ok {
		return false
	}
	types, ok := prMap.vals["types"]
	if !ok {
		return true
	}
	list, isList := types.([]value)
	if !isList {
		list = []value{types}
	}
	return slices.IndexFunc(list, func(t value) bool {
		s, isString := t.(string)
		return !isString || !slices.Contains(allowedTriggers, s)
	}) < 0
}

// permissionsOK: the top level is exactly contents: read and pull-requests: write, and no job
// grants write to anything but pull-requests (the read-all / write-all shorthands are rejected).
func permissionsOK(doc *object, jobs []*object) bool {
	top, ok := doc.vals["permissions"].(*object)
	if !ok || len(top.keys) != 2 || top.vals["contents"] != "read" || top.vals["pull-requests"] != "write" {
		return false
	}
	for _, job := range jobs {
		p, ok := job.vals["permissions"]
		if !ok {
			continue
		}
		pm, ok := p.(*object)
		if !ok {
			return false
		}
		for _, name := range pm.keys {
			if pm.vals[name] == "write" && name != "pull-requests" {
				return false
			}
		}
	}
	return true
}

// secretsOK: no job has a `secrets` key (secrets: inherit), and every reference to `secrets`
// inside an expression names GITHUB_TOKEN or one of names.
func secretsOK(jobs []*object, exprs []string, names []string) bool {
	for _, job := range jobs {
		if _, ok := job.vals["secrets"]; ok {
			return false
		}
	}
	for _, e := range exprs {
		for _, m := range reSecretRef.FindAllStringSubmatch(e, -1) {
			if m[1] == "" || (m[1] != githubToken && !slices.Contains(names, m[1])) {
				return false
			}
		}
	}
	return true
}

// actionsPinned: every job-level and step-level `uses` is a full-commit-SHA pin.
func actionsPinned(jobs []*object) bool {
	for _, job := range jobs {
		if u, ok := job.vals["uses"]; ok && !pinned(u) {
			return false
		}
		steps, _ := job.vals["steps"].([]value)
		for _, s := range steps {
			step, ok := s.(*object)
			if !ok {
				continue
			}
			if u, ok := step.vals["uses"]; ok && !pinned(u) {
				return false
			}
		}
	}
	return true
}

func pinned(uses value) bool {
	s, ok := uses.(string)
	if !ok {
		return false
	}
	m := reAction.FindStringSubmatch(s)
	return m != nil && m[1] != "." && m[1] != ".."
}

// forkGuarded: every job's `if` is the fork guard, with an outer ${{ }} removed and whitespace
// collapsed.
func forkGuarded(jobs []*object) bool {
	for _, job := range jobs {
		cond, ok := job.vals["if"].(string)
		if !ok {
			return false
		}
		bare := jsTrim(cond)
		bare = reOuterExpr.ReplaceAllString(bare, "$1")
		bare = jsTrim(reSpaces.ReplaceAllString(bare, " "))
		if bare != forkGuardExpr {
			return false
		}
	}
	return true
}

func jsTrim(s string) string {
	return strings.TrimFunc(s, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\v', r == '\f', r == '\r', r == ' ',
		r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000, r == 0xfeff:
		return true
	}
	return false
}

// normalize turns a['b'] into a.b, drops whitespace and lower-cases, so notation tricks cannot
// dodge the untrusted-expression check.
func normalize(expr string) string {
	expr = reBracket.ReplaceAllString(expr, ".$1")
	expr = reSpaces.ReplaceAllString(expr, "")
	return jsLower(expr)
}

// jsLower lower-cases like JavaScript's toLowerCase: U+0130 becomes "i" plus a combining dot,
// where Go's unicode.ToLower gives a bare "i".
func jsLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == 0x130 {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// walker walks the value tree, which shares the value of an aliased node, so a walk is bounded by
// a visit budget rather than by the size of the text.
type walker struct {
	visits    int
	exhausted bool
}

func (w *walker) visit() bool {
	w.visits++
	if w.visits > maxVisits {
		w.exhausted = true
	}
	return !w.exhausted
}

// expressions returns every expression body of the document: the value of every `if` key (GitHub
// evaluates it as an expression without ${{ }}) and the body of every ${{ ... }} in a string
// value. Keys are not inspected.
func (w *walker) expressions(doc value) []string {
	out := w.ifValues(doc, nil)
	for _, s := range w.scalars(doc, nil) {
		for _, m := range reExpression.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func (w *walker) scalars(node value, out []string) []string {
	if !w.visit() {
		return out
	}
	switch n := node.(type) {
	case string:
		out = append(out, n)
	case []value:
		for _, v := range n {
			out = w.scalars(v, out)
		}
	case *object:
		for _, k := range n.keys {
			out = w.scalars(n.vals[k], out)
		}
	}
	return out
}

func (w *walker) ifValues(node value, out []string) []string {
	if !w.visit() {
		return out
	}
	switch n := node.(type) {
	case []value:
		for _, v := range n {
			out = w.ifValues(v, out)
		}
	case *object:
		for _, k := range n.keys {
			if s, ok := n.vals[k].(string); ok && k == "if" {
				out = append(out, s)
				continue
			}
			out = w.ifValues(n.vals[k], out)
		}
	}
	return out
}
