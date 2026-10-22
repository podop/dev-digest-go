package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/smartdiff/domain"
)

// The first block is the path table of Node's test/smart-diff-classify.test.ts (the three
// contested cases included); the rest pins the normalisation and the glob semantics.
func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		want domain.Role
		why  string
	}{
		{"pnpm-lock.yaml", domain.RoleBoilerplate, "named lockfile"},
		{"client/pnpm-lock.yaml", domain.RoleBoilerplate, "named lockfile, nested"},
		{"yarn.lock", domain.RoleBoilerplate, "named lockfile"},
		{"packages/app/package-lock.json", domain.RoleBoilerplate, "named lockfile, nested"},
		{"server/foo.lock", domain.RoleBoilerplate, "*.lock basename"},
		{"dist/index.js", domain.RoleBoilerplate, "dist/** root-anchored"},
		{"build/main.js", domain.RoleBoilerplate, "build/** root-anchored"},
		{"schema.generated.ts", domain.RoleBoilerplate, "*.generated.* basename"},
		{"vendor/lib.min.js", domain.RoleBoilerplate, "*.min.js basename"},
		{"src/foo.test.ts", domain.RoleTests, "*.test.ts basename"},
		{"client/src/Foo.test.tsx", domain.RoleTests, "*.test.tsx basename"},
		{"server/test/smart-diff.it.test.ts", domain.RoleTests, "*.it.test.ts basename"},
		{"server/test/helpers/pg.ts", domain.RoleTests, "**/test/** any depth"},
		{"client/src/__tests__/foo.ts", domain.RoleTests, "**/__tests__/** any depth"},
		{"e2e/flows/05-review.flow.json", domain.RoleTests, "e2e/** root-anchored"},
		{"src/api/index.ts", domain.RoleWiring, "index.ts basename"},
		{"client/vite.config.ts", domain.RoleWiring, "*.config.* basename"},
		{"tsconfig.base.json", domain.RoleWiring, "tsconfig*.json basename"},
		{".env.local", domain.RoleWiring, ".env* basename"},
		{".github/workflows/ci.yml", domain.RoleWiring, ".github/** root-anchored"},
		{"README.md", domain.RoleDocs, "README* basename"},
		{"docs/onion-migration.md", domain.RoleDocs, "docs/** root-anchored"},
		{"CHANGELOG.md", domain.RoleDocs, "CHANGELOG* basename"},
		{"LICENSE", domain.RoleDocs, "literal LICENSE"},
		{"src/config.ts", domain.RoleCore, "no pattern matches"},
		{"src/api/handlers.ts", domain.RoleCore, "not a barrel file"},
		{"client/dist/x.js", domain.RoleCore, "dist/** is root-anchored, this is nested"},
		{"__tests__/__snapshots__/x.snap", domain.RoleBoilerplate, "contested: boilerplate before tests"},
		{".claude/skills/security/SKILL.md", domain.RoleWiring, "contested: wiring before docs"},
		{"e2e/README.md", domain.RoleTests, "contested: tests (e2e/**) before docs (README*)"},

		// separators and prefixes
		{"./src/foo.test.ts", domain.RoleTests, "leading ./ removed"},
		{`src\foo.test.ts`, domain.RoleTests, "backslashes are slashes"},
		{`.\src\foo.test.ts`, domain.RoleTests, "windows relative path"},
		{`dist\index.js`, domain.RoleBoilerplate, "windows root-anchored"},
		{"././dist/x.js", domain.RoleCore, "only one leading ./ is removed"},
		{`client\dist\x.js`, domain.RoleCore, "windows nested dist stays core"},

		// nested test directories and anchoring
		{"a/b/test/c.go", domain.RoleTests, "nested test/ dir"},
		{"test/c.go", domain.RoleTests, "root test/ dir"},
		{"tests/c.go", domain.RoleTests, "root tests/ dir"},
		{"src/latest/c.go", domain.RoleCore, "test is a whole segment"},
		{"src/mytest/c.go", domain.RoleCore, "mytest is not test"},
		{"src/e2e/x.ts", domain.RoleCore, "e2e/** is root-anchored"},
		{"docs/a/b.png", domain.RoleDocs, "docs/** any file below"},
		{"src/docs/a.png", domain.RoleCore, "docs/** is root-anchored"},

		// wiring and docs by base name
		{".env", domain.RoleWiring, ".env itself"},
		{"sub/.env.production", domain.RoleWiring, ".env* nested"},
		{"docker-compose.override.yml", domain.RoleWiring, "docker-compose*.yml"},
		{"index.tsx", domain.RoleCore, "index.tsx is not a barrel"},
		{"README.de.md", domain.RoleDocs, "README* and *.md"},
		{"src/README", domain.RoleDocs, "README without extension, nested"},
		{"readme.md", domain.RoleDocs, "*.md is case-sensitive but matches the extension"},
		{"Readme.txt", domain.RoleCore, "case-sensitive README*"},
		{"license", domain.RoleCore, "case-sensitive LICENSE"},
		{"app.config", domain.RoleCore, "*.config.* needs a suffix"},
		{"app.config.js", domain.RoleWiring, "*.config.*"},
		{"x.lock", domain.RoleBoilerplate, "*.lock"},
		{"x.lockfile", domain.RoleCore, "*.lock is the whole base name"},
		{"a.min.js", domain.RoleBoilerplate, "*.min.js"},
		{"a.min.jsx", domain.RoleCore, "*.min.js is the whole base name"},
		{"", domain.RoleCore, "empty path"},

		// a line terminator is not matched by "**"
		{"dist/a\nb", domain.RoleCore, "** does not cross a newline (JavaScript .)"},
		{"dist/a b", domain.RoleCore, "** does not cross U+2028"},
		{"x/a.test.ts\n", domain.RoleCore, "$ is the end of the text only"},
	}
	for _, tc := range tests {
		t.Run(tc.why+" "+tc.path, func(t *testing.T) {
			if got := domain.Classify(tc.path); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
