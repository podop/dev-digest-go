# Context pack: GitLab C
[A] `B/`=backend, `S/`=B/cmd/server, `R/`=B/cmd/devdigest-runner; B/internal: `CI/`=ci, `CR/`=ci/runner, `GL/`=platform/gitlab, `NB/`=GL/notebody, `SP/`=spend · `F/`=frontend/src, `SH/`=F/vendor/shared/contracts, `EW/`=F/app/agents/[id]/…/CiTab/_components/ExportWizard, `SI/`=F/app/settings/[section]/…/SettingsIntegrations/_components, `E/`=frontend/messages/en · B-EC15 = B's corpus ≠ C's EC15

## Lane contracts
- [C1] `SH/eval-ci.ts` = §7.1 + `CiRepoEntry += provider, host, target_type`; hooks take `repo_id`, `target`
- [C2] `CI/domain`: `TargetGitLabCI`, `RunSourceGitLab` ("gitlab_ci"); `GitLabJobPath/JobName(slug)`, `IncludeSnippet`, `BuildGitLabJob(opts)`, `BuildRootInclude`, `BuildMRDescription(MRDescInput)`, `GitLabRequiredSecrets`, `TriggersNote`, `JobImage`
- [C3] `wfrules.CheckGitLab(text, GitLabRules{Slug, CustomEnv, HeaderCount}) Result`; rules structure, include, forbidden_trigger, unpinned_image, variables, untrusted_variable
- [C4] `CI/domain`: `GitLabPipeline`, `GitLabJob`, `MRIIDFromRef`, `VerifyGitLabArtifact`, `NewGitLabRun`, `GitLabJobURL`, §7.1 codes
- [C5] `GL/`: `Project.CIConfigPath`; `Branch`, `FileMeta` (HEAD, content sha256), `Commit`, `OpenMRs`, `CreateMR` (writes on B's `postOnce`), `Pipelines`, `PipelineJobs`, `JobArtifactFile` (storage leg: own client)
- [C6] `CI/app`: `RepoDirectory.Find(ws, {ID, FullName})` (+ host fields, ClonePath; `*AmbiguousRepoError{IDs}`); ports `GitLabConns.Resolve(host)`, `CloneFiles.Exists`, `GitLabPublisher`, `GitLabCI`; `*GitLabError{Status, Transport, Step}`
- [C7] `CR/domain`: `ParseGitLabEnv`, fork/token checks, `PositionIndex(raw).At(file, line)` (old path, old line), `SelectInline(cs, 50)`, `CapSection`, `GitLabLogText`, `StripGitLabIgnored`
- [C8] `CR/app`: `Deps.GitLab` (MR, ListDrafts, CreateDraft, DeleteDraft, PublishDraft, BulkPublish, CreateNote), `Deps.GitDiff` (`*CommitMissingError`)
- [C9] `SP/domain.CIDedupeKeyFor(host, id, att)`: github.com = old key, else `ci:<host>:<id>:<att>`; `CIRun.Host`
- [C-NB] notebody (B S1, committed e0e58b4, frozen): `const MaxChars = 1_000_000`; `const ZWSP = "\u200b"`; `func Apply(body string) string` (B FR14→15→16 on the whole body, linear, idempotent FR17); `func Chars(s string) int` (code points). Corpus `NB/testdata/ec15.json` `[{name,in,want}]`. Package test `TestPackage_ImportsStandardLibraryOnly`

## Repo rules and notes
- [K1] `CLAUDE.md:38-46` wire compat, guards; `:92`; `:90`
- [K2] `B/AGENTS.md:60-73` layout, runner closure, cmd ports
- [K3] `:239-242` apperr, up+down; `:248-250` ReadJSONBody
- [K4] `:211-215` GitLab egress; `:227-230` redaction
- [K5] `:84-100` ledger, CI dedupe key; `:189-193` CI provider
- [K6] `:251-258` `postgres.Conn` in `InTx`, pgtest
- [K7] `B/INSIGHTS.md:150` pinned migration tests; `:68`
- [K8] `:26` signed-URL leg, unwrap `*url.Error`; `:36` no redirects
- [K9] `:69` job log, fork skip; `:115` closure CGO_ENABLED=1; `:74`
- [K10] `:110`, `:114` yaml.v3; `:47` ON CONFLICT + partial index; `:86` savepoint; `:88` 23505
- [K11] `:50` `-run TestCiExport -update`; `:54` `-run Contracts -update`; `:64` UI entry; `:154` agent_runs row, ~80 s export
- [K12] `:146` 25m; `:99` SSL_CERT_FILE; `:143` exact logs; `:100` synctest
- [K13] `frontend/AGENTS.md:13-19`; `F/INSIGHTS.md` :30 :31 :34 :49 :71 :72 :76 :81 :82 :107 :119
- [K14] `B/INSIGHTS.md:89` redact, cut; `:151`; `:21` · [K15] `:53`

## Verified facts (drift since 4819a47)
- [F1] moved: `dto.go` :336-436, `eval-ci.ts` :134-327, INSIGHTS :17→:26, :40→:69, 70/74→110/114
- [F2] A: `CI/adapters/peers/directories.go:113-131` + `ErrUnsupportedHost` (`CI/app/export.go:36-37,103-107`); UI GitHub-only `EW/useExportWizard.ts:62-66`
- [F3] agent → 409 plugin_disabled (`export.go:88-91`) → target → provider
- [F4] GitHub token first (`CI/app/ingest.go:113-115`); gha only (`CI/adapters/postgres/lists.go:82-86`); conflict keys (`…/postgres/ingest.go:134-137`, `export.go:46-53`)
- [F5] ledger key `ci:<id>:<attempt>` global UNIQUE (`SP/domain/ledger.go:92`, `0005:28`)
- [F6] `0001:115-145`, uniques `:811 :825 :832`, `0009:94-100`
- [F7] `CR/app/run.go:61-207` GitHub only; `R/main.go:86-91` stop-commands; `run.go:337-353` redaction; `CR/domain/payload.go:79-131` no `@ < > ![`; `reviewer/domain/model.go:122-138` no old path
- [F8] `R/closure_test.go:14-30` prefixes lack `GL/`; stdlib `NB/` safe; a `GL/` prefix entry would hit `NB/`
- [F9] `GL/client.go:219-250` always retries; `GL/rest.go:73-107` no ci_config_path
- [F10] `wfrules/load.go:81` loader unexported; tags dropped `:355-397`
- [F11] `EW/constants.ts:40`; `F/lib/ci-runs.ts:15-18`; `E/apiKeys.json:10`; `E/automaticReviews.json:9`; no axe dep
- [F12] `repos/domain/repo.go:32-57`, `platform/clonefs/read.go:47`, `R/main.go:62,283`
- Mirrors: ghapi, `platform/github/artifact.go`, `CI/domain/{workflow,prbody}.go`, `R/parity_test.go`, `S/{phase6_ci,gitlab_sync}_test.go`, 0010 down

## Details [D]
- A1 Apply before each create · A2 HEAD file meta · A3 `encoding: text` · A4 sequence retry once, not 429 · A5 P3 indexes + target CHECKs · A6 clonefs.Stat; no clone → create · A7 MR cache per refresh · A8 runner 30 s, GET ×2, system + CI_SERVER_TLS_CA_FILE, origin+prefix · A9 git diff `--no-ext-diff --no-textconv --no-color -M`, no global config · A10 sanitizer covers slog · A11 keyConfigured rule · A12 down refuses · A13 AC37/AC48 need user · A14 redact §7.5 secrets only · A15 engine order = index

## Skill map (`pp:` = `engineering-paved-path:`)
| Steps | Skills | Rules |
|---|---|---|
| S2–S16, S20–S25 | go-architecture, go-testing | stdlib domain, ports; fakes, pgtest, fuzz |
| S2, S13 | pp:postgresql-table-design | NOT NULL + CHECK, partial unique |
| S4, S6–S9, S14, S16, S21 | pp:security | SSRF, tokens, injection |
| S1, S17–S19, S26 | pp:react-best-practices, pp:react-testing-library, pp:typescript-expert | roles |

## Verification sources
[V1] `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9-11`) · [V2] `pnpm exec vitest run <fragment>`, `pnpm typecheck`, `pnpm exec biome lint --error-on-warnings <dir>` (`frontend/AGENTS.md:4`, F/INSIGHTS :72 :76) · [V3] `make backend-check|frontend-check|check` (`Makefile:119,144,149`)
