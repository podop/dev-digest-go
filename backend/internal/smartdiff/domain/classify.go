package domain

import (
	"regexp"
	"strings"
)

// rolePatterns are the fixed globs of each role (Node smart-diff/domain/constants.ts), in the
// order classification tries the roles: boilerplate, tests, wiring, docs. A path that matches none
// is core.
//
// Glob semantics (compileGlob): a leading "**/" matches at any depth; otherwise a pattern with a
// "/" is anchored at the root and one without "/" matches the base name at any depth; "*" is one
// path segment ("[^/]*"), "**" elsewhere is anything; the whole path must match; matching is
// case-sensitive.
var rolePatterns = []struct {
	role     Role
	patterns []string
}{
	{RoleBoilerplate, []string{
		"*.lock", "pnpm-lock.yaml", "package-lock.json", "yarn.lock", "dist/**", "build/**",
		"**/__snapshots__/**", "*.snap", "*.generated.*", "*.min.js",
	}},
	{RoleTests, []string{
		"**/*.test.ts", "**/*.test.tsx", "**/*.it.test.ts", "**/*.spec.ts", "**/test/**", "**/tests/**",
		"**/__tests__/**", "e2e/**",
	}},
	{RoleWiring, []string{
		"index.ts", "index.js", "*.config.*", "tsconfig*.json", ".eslintrc*", ".env*",
		"docker-compose*.yml", ".github/**", ".claude/**",
	}},
	{RoleDocs, []string{"*.md", "docs/**", "README*", "CHANGELOG*", "LICENSE"}},
}

// compiledRoles holds rolePatterns compiled once; it is never modified.
var compiledRoles = compileRoles()

type compiledRole struct {
	role Role
	res  []*regexp.Regexp
}

func compileRoles() []compiledRole {
	out := make([]compiledRole, len(rolePatterns))
	for i, rp := range rolePatterns {
		res := make([]*regexp.Regexp, len(rp.patterns))
		for j, p := range rp.patterns {
			res[j] = compileGlob(p)
		}
		out[i] = compiledRole{role: rp.role, res: res}
	}
	return out
}

// anything is JavaScript's "." without the s flag: every character but the four line terminators.
// Go's "." differs (it excludes only \n), so the class is written out.
const anything = `[^\n\r\x{2028}\x{2029}]`

// compileGlob compiles one fixed glob (never user input) into the regular expression the Node
// classifier builds: "(?:^|.*/)" in front of a body that may match at any depth, "^" in front of
// a root-anchored one, "$" at the end.
func compileGlob(pattern string) *regexp.Regexp {
	anyDepth := strings.HasPrefix(pattern, "**/")
	body := pattern
	if anyDepth {
		body = pattern[3:]
	}
	hasSlash := strings.Contains(body, "/")
	var out strings.Builder
	for i := 0; i < len(body); {
		switch {
		case strings.HasPrefix(body[i:], "**"):
			out.WriteString(anything + "*")
			i += 2
		case body[i] == '*':
			out.WriteString("[^/]*")
			i++
		default:
			out.WriteString(regexp.QuoteMeta(body[i : i+1]))
			i++
		}
	}
	prefix := "^"
	if anyDepth || !hasSlash {
		prefix = "(?:^|" + anything + "*/)"
	}
	return regexp.MustCompile(prefix + out.String() + "$")
}

// normalisePath turns "\" into "/" and removes one leading "./", so the role never depends on how
// the caller built the path.
func normalisePath(path string) string {
	return strings.TrimPrefix(strings.ReplaceAll(path, `\`, "/"), "./")
}

// Classify returns the role of a changed file: the first role in the order boilerplate, tests,
// wiring, docs with a matching glob, else core.
func Classify(path string) Role {
	p := normalisePath(path)
	for _, cr := range compiledRoles {
		for _, re := range cr.res {
			if re.MatchString(p) {
				return cr.role
			}
		}
	}
	return RoleCore
}
