package wfrules

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// This file is the rule checker of a GitLab CI job file (GitLab C, FR14): the generated file of
// spec 7.3 passes, an edited one must keep its shape. It shares the bounded loader with CheckWith
// (load.go: one document, no duplicate keys, alias and visit budgets) and, unlike the GitHub
// rules, looks at tags, which the loader reports because the `include` rule refuses all of them.

// The GitLab rule ids that GitHub's list does not have; ForbiddenTrigger and InvalidYAML are shared.
const (
	Structure         Rule = "structure"
	Include           Rule = "include"
	UnpinnedImage     Rule = "unpinned_image"
	Variables         Rule = "variables"
	UntrustedVariable Rule = "untrusted_variable"
)

// GitLabRules is what one export may contain: the values CheckGitLab compares the file with.
type GitLabRules struct {
	// Slug is the agent slug of the export; the job is devdigest-review-<Slug>.
	Slug string
	// CustomEnv is <S> of the derived names DEVDIGEST_<S>_BASE_URL, _API_KEY and _HEADER_<n> (the
	// slug of a custom provider upper-cased, "-" as "_"). Empty for a built-in provider: the job
	// then maps no provider variables. A value that is not ^[A-Z0-9_]{1,32}$ counts as empty.
	CustomEnv string
	// HeaderCount is the number of extra headers of the custom provider (SCR7): only
	// DEVDIGEST_LLM_HEADER_1 to _<HeaderCount> may appear, at most 5.
	HeaderCount int
}

// gitlabMaxVisits is the visit budget of the two walks over the job (rules 2 and 6) together. A
// file of at most 64 KiB has fewer than 33,000 distinct nodes, so a legitimate file uses a quarter
// of it; aliases make the tree a DAG whose walk can be far larger than the text, and the budget
// keeps a check of such a file within a few milliseconds (NFR5) instead of the loader's 2,000,000.
const gitlabMaxVisits = 250_000

// maxHeaders is the largest number of extra headers a custom provider has (domain.MaxExtraHeaders).
const maxHeaders = 5

// postAsValues are the DEVDIGEST_POST_AS values (domain.PostAs).
var postAsValues = []string{"github_review", "pr_comment", "none"}

// gitlabForbiddenKeys are the keys that pull in other configuration, other jobs or other
// capabilities (rule include), wherever they appear. `when` is the one more: it is refused
// everywhere except in `artifacts` (and in the `rules` entries, where rule 3 judges it).
var gitlabForbiddenKeys = []string{
	"include", "extends", "trigger", "needs", "services", "before_script", "after_script",
	"secrets", "id_tokens", "cache", "hooks",
}

// gitlabJobKeys are the keys the job may use (rule structure).
var gitlabJobKeys = []string{
	"stage", "image", "tags", "inherit", "rules", "interruptible", "timeout", "variables", "script", "artifacts",
}

// gitlabBaseNames are the variables a string of the job file may reference (rule
// untrusted_variable); inside `variables` values the derived provider names are added.
var gitlabBaseNames = []string{
	"CI_PROJECT_DIR", "CI_JOB_ID", "CI_PIPELINE_SOURCE",
	"CI_MERGE_REQUEST_SOURCE_PROJECT_ID", "CI_MERGE_REQUEST_PROJECT_ID",
}

const (
	// gitlabMRRule is the one job rule (spec 7.3; domain.MRRule), compared with whitespace runs
	// collapsed.
	gitlabMRRule = `$CI_PIPELINE_SOURCE == "merge_request_event" && $CI_MERGE_REQUEST_SOURCE_PROJECT_ID == $CI_MERGE_REQUEST_PROJECT_ID`
	// gitlabResultFile is the artifact the runner writes (domain.ResultFileName).
	gitlabResultFile = "devdigest-result.json"
)

var (
	reGitLabTag    = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	reGitLabStage  = regexp.MustCompile(`^[A-Za-z0-9_.-][A-Za-z0-9_. -]{0,63}$`)
	reGitLabImage  = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]{1,128})?@sha256:[0-9a-f]{64}$`)
	reGitLabDepth  = regexp.MustCompile(`^[0-9]+$`)
	reGitLabEnv    = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)
	reGitLabSpaces = regexp.MustCompile(`[ \t\r\n]+`)
)

// CheckGitLab runs the size limit, the YAML syntax and the six GitLab rules of FR14 over a job file
// and returns the first broken one: invalid_yaml, structure, include, forbidden_trigger,
// unpinned_image, variables, untrusted_variable. The size (UTF-8 bytes) is checked before the text
// is parsed.
//
// A document that contains a YAML tag fails `include` instead of `structure` when its shape is
// wrong, because a tag (`!reference` first of all) is what changes the shape of a value; a key
// that `include` forbids does not break `structure` either, so each is named by the rule that
// forbids it.
func CheckGitLab(text string, r GitLabRules) Result {
	if len(text) > maxWorkflowBytes {
		return fail(InvalidYAML)
	}
	root, tagged, err := parseDocument(text, true)
	if err != nil {
		return fail(InvalidYAML)
	}
	env := ""
	if reGitLabEnv.MatchString(r.CustomEnv) {
		env = r.CustomEnv
	}
	jobName := "devdigest-review-" + r.Slug

	job, ok := gitlabStructure(root, r.Slug, jobName)
	if !ok {
		if tagged {
			return fail(Include)
		}
		return fail(Structure)
	}

	w := &walker{visits: maxVisits - gitlabMaxVisits}
	if tagged || w.forbidden(root, false) {
		return fail(Include)
	}
	if w.exhausted {
		return fail(InvalidYAML)
	}
	if !gitlabRulesOK(job.vals["rules"]) {
		return fail(ForbiddenTrigger)
	}
	if !gitlabImageOK(job.vals["image"]) {
		return fail(UnpinnedImage)
	}
	if !gitlabVariablesOK(job.vals["variables"], r.Slug, env, r.HeaderCount) {
		return fail(Variables)
	}

	base := nameSet(gitlabBaseNames)
	withProvider := nameSet(gitlabBaseNames)
	if env != "" {
		withProvider[baseURLName(env)] = true
		withProvider[apiKeyName(env)] = true
		for n := 1; n <= min(max(r.HeaderCount, 0), maxHeaders); n++ {
			withProvider[headerName(env, n)] = true
		}
	}
	for _, k := range job.keys {
		allowed := base
		if k == "variables" {
			allowed = withProvider
		}
		if !w.referencesAllowed(job.vals[k], allowed) {
			if w.exhausted {
				return fail(InvalidYAML)
			}
			return fail(UntrustedVariable)
		}
	}
	if !gitlabShellOK(job) {
		return fail(UntrustedVariable)
	}
	return Result{OK: true}
}

// gitlabShellOK is the second half of rule 6: the strings the shell reads - the members of
// `script` and of `image.entrypoint` - must not read variables around the written-out reference
// forms of referencesOK. Rule 6 is a lint, not a sandbox: it refuses the ways to reach a
// variable's value without naming it that are cheap to detect, and fails closed on them.
// Aliases make the same string appear many times; each distinct string is scanned once, so the
// pass costs at most the size of the document (NFR5).
func gitlabShellOK(job *object) bool {
	var lines []value
	lines, _ = job.vals["script"].([]value)
	if img, ok := job.vals["image"].(*object); ok {
		if e, ok := img.vals["entrypoint"].([]value); ok {
			lines = append(slices.Clone(lines), e...)
		}
	}
	seen := make(map[string]bool, len(lines))
	for _, l := range lines {
		s, ok := l.(string)
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		if !shellReadsNoVariables(s) {
			return false
		}
	}
	return true
}

// gitlabEnvCommands are the words that print, dump or evaluate variables: printenv, env, eval,
// declare/typeset/set/export/readonly/compgen (their no-argument and -p/-v forms dump every
// variable) and alias (it can give any of them another name) - set variables in `variables`, not
// in the script. `set` and `export` are the two that a script legitimately needs, so they are
// allowed in the two forms that cannot dump (see shellArgsOK); everything else on the list is
// refused wherever the word stands.
var gitlabEnvCommands = []string{"printenv", "env", "eval", "declare", "typeset", "set", "export", "readonly", "compgen", "alias"}

var (
	reShellFlags  = regexp.MustCompile(`^[-+][A-Za-z]+$`)
	reShellOption = regexp.MustCompile(`^[A-Za-z]+$`)
	reShellExport = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(=.*)?$`)
)

// namesEnviron reports whether s holds the word "environ" (any case) not followed by a letter.
func namesEnviron(s string) bool {
	lower := strings.ToLower(s)
	for from := 0; ; {
		i := strings.Index(lower[from:], "environ")
		if i < 0 {
			return false
		}
		end := from + i + len("environ")
		if end == len(lower) || lower[end] < 'a' || lower[end] > 'z' {
			return true
		}
		from = end
	}
}

// shellComment marks the start of a shell comment in the plain text (see shellPlain).
const shellComment = "\x00"

// shellPlain removes the quote characters and backslashes of the script line s, which is how
// every check but the substitution ones reads it, and puts shellComment before each `#` that
// starts a comment. The shell starts a comment only at an unquoted, unescaped `#` at the start
// of a word - after white space or one of ; & | ( ) < > or at the start of the line - and the
// comment lasts to the end of the line: "#", '#', \# and a#b are ordinary words. The decision
// is made here, on the raw text, never on the text after the quotes are gone. The quote state
// is carried over line breaks, as in the shell; s holds no NUL (shellReadsNoVariables).
func shellPlain(s string) string {
	var out strings.Builder
	emit := func(c byte) {
		if c != '\'' && c != '"' && c != '\\' {
			out.WriteByte(c)
		}
	}
	var single, double, escaped bool
	wordStart := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
			emit(c)
			wordStart = false
		case single:
			single, wordStart = c != '\'', false
			emit(c)
		case double:
			wordStart = false
			switch c {
			case '"':
				double = false
			case '\\':
				escaped = true
			default:
				emit(c)
			}
		case c == '\\':
			escaped, wordStart = true, false
		case c == '\'':
			single, wordStart = true, false
		case c == '"':
			double, wordStart = true, false
		case c == '#' && wordStart:
			out.WriteString(shellComment)
			for ; i < len(s) && s[i] != '\n'; i++ {
				emit(s[i])
			}
			i-- // the line break, if any, is read by the loop
			wordStart = false
		default:
			emit(c)
			wordStart = strings.IndexByte(" \t\n;&|()<>", c) >= 0
		}
	}
	return out.String()
}

// namesEnvCommand reports whether a word of s, split at white space and at { } , $ ! # and =
// (so the value of an assignment, X=printenv, is a word of its own), is a command of
// gitlabEnvCommands once a path is reduced to its last element (/usr/bin/env).
func namesEnvCommand(s string) bool {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("{},$!#=", r)
	})
	for _, word := range words {
		if i := strings.LastIndexByte(word, '/'); i >= 0 {
			word = word[i+1:]
		}
		if slices.Contains(gitlabEnvCommands, word) {
			return true
		}
	}
	return false
}

// shellArgsOK reports whether the arguments of a leading `set` or `export` (the words up to the
// end of the command or a comment, see shellPlain) cannot make it dump the variables:
//   - set: at least one argument, every one a flag cluster such as -e, -eu, +x, -euo, and a cluster
//     ending in o (-o, +o, -eo) followed by an option name (pipefail). set -- and set with a
//     value, a reference or a glob are refused, as is bare set;
//   - export: at least one argument, every one NAME or NAME=value with NAME in
//     [A-Za-z_][A-Za-z0-9_]*, none of whose words (NAME, the value) is a command of
//     gitlabEnvCommands. The value's references are judged by referencesOK.
func shellArgsOK(cmd string, args []string) bool {
	if i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, shellComment) }); i >= 0 {
		args = args[:i]
	}
	if len(args) == 0 {
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if cmd == "export" {
			if !reShellExport.MatchString(a) || namesEnvCommand(a) {
				return false
			}
			continue
		}
		if !reShellFlags.MatchString(a) {
			return false
		}
		if strings.HasSuffix(a, "o") {
			i++
			if i == len(args) || !reShellOption.MatchString(args[i]) {
				return false
			}
		}
	}
	return true
}

// shellReadsNoVariables reports whether the script line s contains none of the indirect reads.
// Backslash-newline continuations are removed first (the shell removes them before it splits
// words), then:
//   - a NUL byte (it marks comments in shellPlain);
//   - command substitution `$(` (also `$((`; so `$(pwd)` is refused too - a substitution can hide
//     a read, and the script has no need for one) and backticks, process substitution `<(` and
//     `>(`;
//   - ANSI-C quoting `$'`, which writes a name as escapes ($'\x24NAME'); `${!` indirection is
//     refused by referencesOK;
//   - the positional lists $@ and $* (also ${@}, ${*}, "$@"): they put arbitrary words back
//     together, so "$@"env can spell a command word that no scan sees;
//   - brace expansion with a comma or `..` (e{,}nv spells env);
//   - the word "environ" in any case, not followed by a letter (/proc/self/environ, ENVIRON[..],
//     os.environ; "environment" passes), also once quotes and backslashes are removed (an empty quote pair inside the word);
//   - a command word of gitlabEnvCommands, after quotes and backslashes are removed (a quote or a
//     backslash inside the name does not hide it) and with a path reduced to its last element
//     (/usr/bin/env); words are split at white space and at ; & | ( ) < > { } , $ ! # and =, so
//     the value of an assignment (X=printenv, alias p=printenv) is a word of its own.
//     The exception is `set` / `export` as the first word of a command (commands end at
//     ; & | ( ) < > and line breaks) when shellArgsOK accepts the arguments: set -e,
//     set -euo pipefail, export NAME=value. A `#` ends the arguments only when it starts a
//     comment of the raw line (shellPlain).
//
// Glob spellings (/usr/bin/e?v) and interpreters (python -c) are out of scope: they need a
// shell-aware parser, and the job file is the user's own, edited through the local API.
func shellReadsNoVariables(s string) bool {
	s = strings.NewReplacer("\\\r\n", "", "\\\n", "").Replace(s)
	for _, bad := range []string{"\x00", "$(", "`", "$'", "<(", ">(", "$@", "$*", "${@", "${*"} {
		if strings.Contains(s, bad) {
			return false
		}
	}
	if namesEnviron(s) {
		return false
	}
	inGroup := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '{' && (i == 0 || s[i-1] != '$'):
			inGroup = true
		case s[i] == '}':
			inGroup = false
		case inGroup && (s[i] == ',' || s[i] == '.' && i+1 < len(s) && s[i+1] == '.'):
			return false
		}
	}
	plain := shellPlain(s)
	if namesEnviron(plain) {
		return false
	}
	commands := strings.FieldsFunc(plain, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '(' || r == ')' || r == '<' || r == '>' || r == '\n' || r == '\r'
	})
	for _, command := range commands {
		fields := strings.FieldsFunc(command, unicode.IsSpace)
		if len(fields) > 0 && (fields[0] == "set" || fields[0] == "export") {
			if !shellArgsOK(fields[0], fields[1:]) {
				return false
			}
			continue
		}
		if namesEnvCommand(command) {
			return false
		}
	}
	return true
}

// baseURLName, apiKeyName and headerName are the derived names for <S> = env (spec 7.3, FR10); they
// equal domain.BaseURLVariable, APIKeySecret and HeaderSecret, which this package cannot import.
func baseURLName(env string) string { return "DEVDIGEST_" + env + "_BASE_URL" }

// apiKeyName: see baseURLName.
func apiKeyName(env string) string { return "DEVDIGEST_" + env + "_API_KEY" }

// headerName: see baseURLName.
func headerName(env string, n int) string { return "DEVDIGEST_" + env + "_HEADER_" + strconv.Itoa(n) }

func nameSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names)+8)
	for _, n := range names {
		m[n] = true
	}
	return m
}

// deferred reports whether a key belongs to rule `include` (rule 2) and so is not judged by
// `structure`.
func deferred(key string) bool {
	return key == "when" || slices.Contains(gitlabForbiddenKeys, key)
}

// strictKeys reports whether the keys of o, leaving out the ones `include` judges, are all in
// allowed and every one of required is present.
func strictKeys(o *object, allowed, required []string) bool {
	for _, k := range o.keys {
		if !slices.Contains(allowed, k) && !deferred(k) {
			return false
		}
	}
	for _, k := range required {
		if _, ok := o.vals[k]; !ok {
			return false
		}
	}
	return true
}

// gitlabStructure is rule 1 and returns the job.
func gitlabStructure(root value, slug, jobName string) (*object, bool) {
	doc, ok := root.(*object)
	if !ok {
		return nil, false
	}
	if !strictKeys(doc, []string{jobName}, []string{jobName}) {
		return nil, false
	}
	job, ok := doc.vals[jobName].(*object)
	if !ok || !strictKeys(job, gitlabJobKeys, []string{"inherit", "script", "artifacts"}) {
		return nil, false
	}

	if s, has := job.vals["stage"]; has {
		if str, ok := s.(string); !ok || !reGitLabStage.MatchString(str) {
			return nil, false
		}
	}
	if t, has := job.vals["tags"]; has && !gitlabTagsOK(t) {
		return nil, false
	}
	if i, has := job.vals["interruptible"]; has {
		if _, ok := i.(bool); !ok {
			return nil, false
		}
	}
	if t, has := job.vals["timeout"]; has {
		if _, ok := t.(string); !ok {
			return nil, false
		}
	}
	if !gitlabInheritOK(job.vals["inherit"]) || !gitlabScriptOK(job.vals["script"]) || !gitlabArtifactsOK(job.vals["artifacts"], slug) {
		return nil, false
	}
	return job, true
}

func gitlabTagsOK(v value) bool {
	list, ok := v.([]value)
	if !ok || len(list) < 1 || len(list) > 10 {
		return false
	}
	return !slices.ContainsFunc(list, func(t value) bool {
		s, ok := t.(string)
		return !ok || !reGitLabTag.MatchString(s)
	})
}

// gitlabInheritOK: inherit is exactly {default: false, variables: false}.
func gitlabInheritOK(v value) bool {
	o, ok := v.(*object)
	return ok && strictKeys(o, []string{"default", "variables"}, []string{"default", "variables"}) &&
		o.vals["default"] == false && o.vals["variables"] == false
}

func gitlabScriptOK(v value) bool {
	list, ok := v.([]value)
	return ok && len(list) > 0 && gitlabStringList(v)
}

// gitlabStringList: v is a list (possibly empty) whose members are all strings.
func gitlabStringList(v value) bool {
	list, ok := v.([]value)
	return ok && !slices.ContainsFunc(list, func(l value) bool { _, ok := l.(string); return !ok })
}

// gitlabArtifactsOK: when: always, name: devdigest-result-<slug>, paths: [devdigest-result.json] and
// an optional expire_in. `when` is the one key of `include` that lives here.
func gitlabArtifactsOK(v value, slug string) bool {
	o, ok := v.(*object)
	if !ok {
		return false
	}
	for _, k := range o.keys {
		if !slices.Contains([]string{"when", "name", "paths", "expire_in"}, k) && !deferred(k) {
			return false
		}
	}
	paths, _ := o.vals["paths"].([]value)
	if o.vals["when"] != "always" || o.vals["name"] != "devdigest-result-"+slug || len(paths) != 1 || paths[0] != gitlabResultFile {
		return false
	}
	if e, has := o.vals["expire_in"]; has {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}

// forbidden reports whether any key of rule `include` appears in the tree. allowWhen is true for
// the keys of an `artifacts` mapping and of `rules` entries.
func (w *walker) forbidden(node value, allowWhen bool) bool {
	if !w.visit() {
		return false
	}
	switch n := node.(type) {
	case []value:
		for _, v := range n {
			if w.forbidden(v, allowWhen) {
				return true
			}
		}
	case *object:
		for _, k := range n.keys {
			if slices.Contains(gitlabForbiddenKeys, k) || (k == "when" && !allowWhen) {
				return true
			}
			if w.forbidden(n.vals[k], k == "artifacts" || k == "rules") {
				return true
			}
		}
	}
	return false
}

// gitlabRulesOK is rule 3: one entry with the one key `if`, the MR rule of spec 7.3.
func gitlabRulesOK(v value) bool {
	list, ok := v.([]value)
	if !ok || len(list) != 1 {
		return false
	}
	entry, ok := list[0].(*object)
	if !ok || len(entry.keys) != 1 {
		return false
	}
	cond, ok := entry.vals["if"].(string)
	if !ok {
		return false
	}
	return strings.Trim(reGitLabSpaces.ReplaceAllString(cond, " "), " ") == gitlabMRRule
}

// gitlabImageOK is rule 4: a string, or {name, entrypoint?} with a name pinned by digest and no `$`.
func gitlabImageOK(v value) bool {
	name, ok := v.(string)
	if o, isMap := v.(*object); isMap {
		if len(o.keys) < 1 || len(o.keys) > 2 {
			return false
		}
		for _, k := range o.keys {
			if k != "name" && k != "entrypoint" {
				return false
			}
		}
		if e, has := o.vals["entrypoint"]; has && !gitlabStringList(e) {
			return false
		}
		name, ok = o.vals["name"].(string)
	}
	return ok && !strings.Contains(name, "$") && reGitLabImage.MatchString(name)
}

// gitlabVariablesOK is rule 5: a mapping of the allowed keys, each with exactly its value.
func gitlabVariablesOK(v value, slug, env string, headerCount int) bool {
	o, ok := v.(*object)
	if !ok {
		return false
	}
	want := map[string]func(string) bool{
		"GIT_DEPTH":          reGitLabDepth.MatchString,
		"DEVDIGEST_PLATFORM": func(s string) bool { return s == "gitlab_ci" },
		"DEVDIGEST_AGENT":    func(s string) bool { return s == slug },
		"DEVDIGEST_POST_AS":  func(s string) bool { return slices.Contains(postAsValues, s) },
	}
	if env != "" {
		exactly := func(name string) func(string) bool { return func(s string) bool { return s == "$"+name } }
		want["DEVDIGEST_LLM_BASE_URL"] = exactly(baseURLName(env))
		want["DEVDIGEST_LLM_API_KEY"] = exactly(apiKeyName(env))
		for n := 1; n <= min(max(headerCount, 0), maxHeaders); n++ {
			want["DEVDIGEST_LLM_HEADER_"+strconv.Itoa(n)] = exactly(headerName(env, n))
		}
	}
	for _, k := range o.keys {
		check, ok := want[k]
		if !ok {
			return false
		}
		if s, isString := o.vals[k].(string); !isString || !check(s) {
			return false
		}
	}
	return true
}

// referencesAllowed is rule 6 over a subtree: every string scalar (keys are not inspected)
// references only names in allowed.
func (w *walker) referencesAllowed(node value, allowed map[string]bool) bool {
	if !w.visit() {
		return false
	}
	switch n := node.(type) {
	case string:
		return referencesOK(n, allowed)
	case []value:
		for _, v := range n {
			if !w.referencesAllowed(v, allowed) {
				return false
			}
		}
	case *object:
		for _, k := range n.keys {
			if !w.referencesAllowed(n.vals[k], allowed) {
				return false
			}
		}
	}
	return true
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// referencesOK finds the variable references of s - $NAME, ${NAME} (also ${NAME:-x}, ${NAME#x}),
// %NAME% and $env:NAME, with $$ a literal dollar - and reports whether all name an allowed
// variable. It reads on after a name, so an inner reference (${A:-$B}, %A%B%) is found too, and a
// "${" without a name is refused: the shell reads it as an expansion it cannot be told apart from.
// The scan is linear: a name never contains the characters that start a reference.
func referencesOK(s string, allowed map[string]bool) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '$':
			if i+1 < len(s) && s[i+1] == '$' {
				i++
				continue
			}
			j := i + 1
			braced := j < len(s) && s[j] == '{'
			if braced {
				j++
			}
			k := j
			for k < len(s) && isNameByte(s[k]) {
				k++
			}
			name := s[j:k]
			if !braced && strings.EqualFold(name, "env") && k+1 < len(s) && s[k] == ':' && isNameByte(s[k+1]) {
				m := k + 1
				for m < len(s) && isNameByte(s[m]) {
					m++
				}
				name, k = s[k+1:m], m
			}
			if name == "" {
				if braced {
					return false
				}
				continue
			}
			if !allowed[name] {
				return false
			}
			i = k - 1
		case '%':
			j := i + 1
			k := j
			for k < len(s) && isNameByte(s[k]) {
				k++
			}
			if k > j && k < len(s) && s[k] == '%' && !allowed[s[j:k]] {
				return false
			}
			i = k - 1
		}
	}
	return true
}
