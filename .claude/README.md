# Local Go harness

Project-level Claude Code components for this Go repository. They sit next to the
user-installed dev-digest plugins and replace their Node/TypeScript backend guidance.

| Component | Path | Origin |
|---|---|---|
| `architecture-reviewer` agent | `agents/architecture-reviewer.md` | adapted from `architecture-review` 1.0.0 (`podop/dev-digest-ai-marketplace`, MIT): Go rule sources and checks, read-only Go commands, fallback lens = the two skills below |
| `go-architecture` skill | `skills/go-architecture/` | new; Three Dots Labs, Google Go Style Guide, Ardan Labs Service, cross-checked with go.dev and others (`references/sources.md`) |
| `go-testing` skill | `skills/go-testing/` | new; Google Go Style Guide, Go wiki/blog, Three Dots Labs (`references/sources.md`) |

How the pieces connect: `CLAUDE.md` tells every agent (including the `sdd-engineering`
planner and implementer) to load the Go skills for Go files, and to use this
`architecture-reviewer` instead of the plugin one.

Conventions kept from the marketplace guidelines: short `SKILL.md` with references one
level deep, descriptions with "Use when … / Not for …", repository rules override the
skills, the reviewer stays read-only (Write/Edit disallowed) and reports only findings
tied to a rule.

All code snippets in the skills were compiled and run (`go vet`, `go test -race
-shuffle=on`, fuzz, benchmark) on Go 1.27.3; the testcontainers helper was compiled
against testcontainers-go v0.44.0 but not run against Docker.

Smoke test for the reviewer: copy `.claude/` into a scratch Go repository with a known
layer violation and run
`claude -p "Use the architecture-reviewer agent to review the uncommitted changes against main."`.
