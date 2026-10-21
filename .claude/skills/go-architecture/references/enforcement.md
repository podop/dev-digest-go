# Enforcement

## Contents
1. Discovering the repository's checks
2. go-arch-lint
3. golangci-lint depguard
4. Grep checks for a diff (no config)

## 1. Discovering the repository's checks

Look, in this order, for the command the repository already uses:
`CLAUDE.md` / `AGENTS.md` → `Makefile` / `Taskfile.yml` / `magefile.go` → CI workflows
(`.github/workflows/*.yml`) → config files at the root:

| File | Tool | Typical command |
|---|---|---|
| `.go-arch-lint.yml` | go-arch-lint | `go-arch-lint check` |
| `.golangci.yml` / `.golangci.yaml` with `depguard` rules | golangci-lint | `golangci-lint run ./...` |
| `go.mod` `tool` directive listing a linter | any | `go tool <name> …` |

Always available, writes nothing tracked: `go build ./...`, `go vet ./...`
(set `GOFLAGS=-mod=readonly` so `go.mod`/`go.sum` stay untouched).

Not configured → say "layer check: not configured" and use section 4. Don't create a
config file during a review; propose one as a follow-up.

## 2. go-arch-lint

Schema `version: 3`. Command `go-arch-lint check` (flags `--arch-file`, `--project-path`,
`--json`). Starting point for the default layout (adjust globs to the repository):

```yaml
version: 3
workdir: internal
allow:
  depOnAnyVendor: false
excludeFiles:
  - "^.*_test\\.go$"
components:
  domain:    { in: "*/domain/**" }
  app:       { in: "*/app/**" }
  adapters:  { in: "*/adapters/**" }
  transport: { in: "*/transport/**" }
  platform:  { in: "platform/**" }
commonComponents:
  - platform
vendors:
  pgx: { in: "github.com/jackc/pgx/**" }
deps:
  domain:    { mayDependOn: [] }
  app:       { mayDependOn: [domain] }
  adapters:  { mayDependOn: [app, domain], canUse: [pgx] }
  transport: { mayDependOn: [app, domain] }
```

Verify the file against the installed version (`go-arch-lint check` reports schema errors)
before relying on it.

## 3. golangci-lint depguard

golangci-lint v2 config (`version: "2"`); `pkg` is a prefix match, `$gostd` is the
standard library:

```yaml
version: "2"
linters:
  enable: [depguard]
  settings:
    depguard:
      rules:
        domain-pure:
          list-mode: strict
          files: ["**/internal/*/domain/**", "!$test"]
          allow: [$gostd]
          deny:
            - { pkg: database/sql, desc: "domain is pure; use a port in app" }
            - { pkg: net/http, desc: "domain is pure; transport maps HTTP" }
        app-no-infra:
          files: ["**/internal/*/app/**", "!$test"]
          deny:
            - { pkg: database/sql, desc: "app talks to storage through a port" }
            - { pkg: net/http$, desc: "HTTP belongs in transport" }
            - { pkg: github.com/jackc/pgx, desc: "drivers live in adapters" }
```

Useful companions when the repository already uses them: `gochecknoglobals`,
`gochecknoinits`, `forbidigo` (forbid `os.Getenv` outside `cmd/`), `errorlint`,
`contextcheck`, `noctx`.

## 4. Grep checks for a diff (no config)

Run on the changed Go files only (`git diff --name-only <base> -- '*.go'` plus untracked
`*.go`). Each hit is a **candidate**; re-read the line and check the package's layer before
reporting it.

| Check | Command (per file or over the list) | Violation when |
|---|---|---|
| Inward-to-outward import | `go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/...` | a `domain` package imports any module package, `database/sql`, `net/http`, `os`; an `app` package imports `adapters`, `transport`, a driver or `net/http` |
| Import of `cmd/` | `grep -n '"<module>/cmd/' <files>` | any hit |
| adapters ↔ transport | `go list` output above | either imports the other |
| Globals / init | `grep -nE '^func init\(\)|^var [a-z]' <files>` | mutable package state (not `Err*`, `MustCompile`, `var _ I = …`) |
| Env / flags outside cmd | `grep -nE 'os\.Getenv|os\.LookupEnv|flag\.(String|Int|Bool|Parse)' <files>` | file not under `cmd/` |
| Exit / fatal outside main | `grep -nE 'os\.Exit|log\.Fatal' <files>` | file not in package `main` |
| Context misuse | `grep -nE 'context\.(Background|TODO)\(\)' <files>` and struct fields of type `context.Context` | non-main, non-test code |
| Tx leak into app/domain | `grep -nE '\*sql\.(Tx|DB)|pgx\.Tx' <files under app/ or domain/>` | any hit |
| Tags on domain types | `grep -nE '(json|db|gorm):"' <files under domain/>` | any hit |
| String error matching | `grep -nE 'err\.Error\(\) ==|strings\.Contains\(err\.Error' <files>` | any hit |
| Grab-bag packages | new directories named `util`, `common`, `helpers`, `misc`, `models`, `types`, `pkg` | any |

`go list` needs a buildable module; if it fails, fall back to reading the `import` blocks
of the changed files.
