# Development Plan: GitLab B — MR comments, note body rules, context peers
Plan: GitLab B · Status: done (2026-10-27; e0e58b4 S1, 76e32dd W1–W3, b19dcd1 W4, 8e990e9 S24, fix r1 41d2ce3, r2 5b08094, r3 a6ebe77; gate arch r1 PASS, security r3 PASS, plan-verify r4 no FAIL; S25 stub run done, AC34 live pending the user; spec D31–D40) · Earlier: approved (user pre-approval 2026-10-27: no CRITICAL items; SCR1–SCR7, A1–A12 as planned; SCR8 conditional on AC34; run autonomously) · Spec: specs/2026-10-24-gitlab-b-comments-context.md · Base: feat/go-backend-phase-1@21a3ce4
Packages: backend, frontend · Context pack: docs/plans/2026-10-27-gitlab-b-comments-context.context.md
Execution: multi-agent, parallel lanes (user default)

## Goal
GitLab MR diff threads listed; findings, comments and replies posted as positioned discussions; every note body neutralised on the server (quick actions, mentions, HTML, images); prior MRs, brief issue, intent tickets/docs, skill import and smart-diff roles work for GitLab.

## Out of scope
Spec §3; slice C (CI, runner, draft notes); `F/vendor/ui`; any `SH/contracts` change.

## Decisions (orchestrator-fixed: P1–P3)
- P1 Migration **0011** (C takes 0012; B lands first); tests pinned to a fixed set + `TestMigration0011` up/down/up [K5]
- P2 B owns FR13–FR17 + EC15 corpus in stdlib leaf `NB/` (imports std only; corpus `NB/testdata/`); API frozen in [C1]; C's runner imports it [F9]
- P3 Order B → C; S1 is committed alone as soon as green, C may start then
- P4 POSTs on a no-retry client path; post-flow GETs retry (A NFR5); budgets count logical requests (SCR2) [C2]
- P5 Discussions paged by page number 1..n, stop on a short page, never `x-next-page` (SCR1) [F3]
- P6 New pulls port `GitLabComments` beside `GitLab` (A fakes untouched); FR6/FR3/FR4/FR10 rules pure in `PU/domain` [C3][C4]
- P7 `html_url` and the intent "same project" rule read the stored `repos.web_url` (refreshed by A FR47) [F5]
- P8 Errors: apperr InvalidInput (400) + code + `details` map{reason, cause[, retry_after_s int]}; cause redacted with the service redactor (all stored tokens) before the cut [K3]
- P9 Context peers: one consumer-side port per context; `GL.Binding` value + resolve funcs bound in `S/gitlab.go` [C5]
- P10 Smart diff: `ClassifyFor/BuildFor(provider,…)`; `Classify/Build` keep GitHub output (parity goldens) [F7]
- P11 Skill import: connection match before `ParseImportURL` (it refuses non-443 and private IPs); FR31 rewrite inside it for non-GitHub hosts [F8]
- P12 UI: one toast (mutation quiet + own toast); refetch on `diff_changed|diff_not_ready` in the hook; host copy via `isGitLab` [K9]
- P13 Diff head stored only when 40/64 lower hex, else NULL; previous path = GitLab `old_path`

## Steps
Done when = the step's tests via [V1]/[V2] + build/vet or typecheck/lint green; UI steps + pseudo-messages [K9]. `+t` = with tests.

### S0 Base check [W0, orchestrator]
0011 free; re-check [F]; brief lanes with [C1]–[C6]
### S1 Note body rules [W1-a]
A `NB/notebody.go`+t, `NB/testdata/ec15.json`, fuzz (idempotent, linear), bench 1 MiB · Covers FR13–FR17, EC15, EC14 (count), AC12, AC14 (rules part), NFR2, NFR8
### S2 Migration 0011 [W1-b]
A `B/migrations/0011_gitlab_diff_positions.{up,down}.sql`; M `MG/migrate_test.go` [K5][D1] · Covers FR34, AC30 (schema)
### S3 GitLab client: notes, refs, files [W1-c]
A `GL/notes.go`+t, `GL/files.go`+t; M `GL/rest.go` (DiffRefs, File.OldPath), `GL/client.go` (postOnce, raw GET, RetryAfterSeconds, InsufficientScope) [C2][K6] · Covers FR7–FR9, FR11, FR24, FR29, FR30 (fetch), NFR3, NFR4
### S4 Pulls domain [W1-d]
A `PU/domain/gitlab_comments.go`+t (fuzz patch map); M `PU/domain/pulls.go` [C3] · Covers FR2–FR4, FR6, FR10 (table), EC1–EC5, EC8, AC3, AC7 (rules)
### S5 Smart-diff roles [W1-e]
M `SD/domain/{classify,build}.go`+t, `SD/app/{service,ports}.go`+t, `SD/adapters/postgres/store.go`+t · Covers FR32, EC26, AC28
### S6 Intent link forms [W1-f]
A `IN/domain/gitlab_links.go`+t (EC20 golden); M `IN/domain/model.go` (Repo.WebURL) · SCR3 · Covers FR26, FR28, EC20, AC23
### S7 Skill URL forms [W1-g]
A `SK/domain/gitlaburl.go`+t; M `SK/domain/urlhost.go`+t (FR31) · Covers FR30 (forms, splits), FR31, EC24, EC25, AC27 (parse)
### S8 UI body + hook [W1-F1]
M `PR/DiffTab/_components/InlineFindingCard/helpers.ts`+t, `F/lib/hooks/reviews.ts`+t; A `F/lib/code-host/comment-errors.ts`+t [C6] · Covers FR18, FR20, AC17 (body)
### S9 UI identity line [W1-F2]
M `SG/_components/{ConnectionRow,TestResult}/**`+t, `E/gitlabIntegration.json` · Covers FR22, EC27, AC20, AC33
### S10 UI peers copy [W1-F3]
M `E/skills.json`, `F/app/skills/_components/ImportUrlModal/**`+t; links already by A tests [F6] · Covers FR33, AC29
### S11 Pulls GitLab adapter [W2-h]
A `PU/adapters/gitlab/comments.go`+t; M `PU/adapters/gitlab/adapter.go`+t (Get: DiffHead, PreviousPath) · Covers FR1, FR7–FR9, FR35, NFR3
### S12 Pulls store [W2-i]
M `PU/adapters/postgres/store.go`+t (diff_head, previous_path, repo web_url) [K4] · Covers FR35, AC6 (store), AC30 (refill)
### S13 Pulls use cases [W2-j]
A `PU/app/gitlab_comments.go`+t, `PU/app/gitlab_comment_ports.go`; M `PU/app/service.go`+t, `PU/app/service_gitlab_test.go` [C4] · Covers FR1, FR5, FR7–FR12, EC6–EC14, EC16, NFR1, NFR4, NFR6, NFR8, AC1–AC11, AC13, AC14, AC16 (unit)
### S14 Prior MRs [W2-k]
M `BL/domain/model.go`, `BL/app/{ports,service}.go`+t, `BL/adapters/postgres/store.go`+t; A `BL/adapters/peers/gitlab.go`+t · Covers FR23, FR24, EC19, AC21 (unit)
### S15 Brief [W2-l]
M `BR/app/{ports,facts,service}.go`+t, `BR/adapters/peers/peers.go`+t (GitLab issue, `RoleFor`) · Covers FR23, FR25, FR32 (brief), AC22 (unit)
### S16 Intent reads [W2-m]
M `IN/app/stale.go`+t, `IN/adapters/peers/{tickets,docs}.go`+t, `IN/adapters/postgres/store.go`+t · Covers FR23, FR27, FR29, EC21, EC22, AC24, AC25 (unit)
### S17 Skill import [W2-n]
M `SK/app/importer.go`+t; A `SK/adapters/gitlabfiles/files.go`+t · Covers FR30, EC23, EC25, NFR3, AC26, AC27 (unit)
### S18 UI posting [W2-F4]
M `PR/DiffTab/{DiffTab.tsx,DiffTab.test.tsx}`, `PR/DiffTab/_components/InlineFindingCard/InlineFindingCard.tsx`, `PR/FindingCard/**`, `DV/{comments.ts,CommentCard,InlineComposer,OutdatedComments}/**`, `E/{prReview,shell,prPosting}.json` +t · Covers FR19–FR21, EC18, NFR9, NFR10, AC17–AC19, AC33
### S19 Composition root [W3-x]
M `S/gitlab.go`, `S/app.go`, `S/gitlab_wiring_test.go`, `B/AGENTS.md` [C5] · Done when `make backend-check` [V3]
### S20 Comment component tests [W4-1]
A `S/gitlab_comments_test.go` (TLS stub, prefix `gb…`) · Covers AC1–AC9, AC11, AC13–AC15, AC30
### S21 Failure + leak tests [W4-2]
A `S/gitlab_comment_errors_test.go` (`gf…`); M `S/gitlab_isolation_test.go` (AC22 of A: GitLab features now call GitLab, still no GitHub) · Covers AC10, AC16, AC32, NFR1
### S22 Peer component tests [W4-3]
A `S/gitlab_peers_test.go` (`gp…`) · Covers AC21, AC22, AC24, AC25
### S23 Skill import component tests [W4-4]
A `S/gitlab_skill_import_test.go` (`gk…`, fake DNS [K7]) · Covers AC26, AC27
### S24 Goldens + contract test [W5-5]
A goldens `gitlab_comments_*`; M `F/test/go-backend-contracts.test.ts` [K8] · Covers AC31, NFR7
### S25 Manual [W5-6]
Throwaway stack [K10]; stub run + AC34 checklist for the user (SCR8) · Covers AC34 · Done when reported

## Execution
- Mode: multi-agent (~70 files, 3 stacks of contexts)
- W0 S0 · W1 a–g ∥ F1–F3 (10 lanes) · W2 h–n ∥ F4 (8) · W3 x · W4 1 ∥ 2 ∥ 3 ∥ 4 · W5 5 ∥ 6
- Lanes share no file; W2 codes against [C1]–[C6]; commit S1 alone, then W1–W3 as one change set, W4, W5 each

## Contracts & migrations
No shared-contract change. [C1] consumed by S13 and slice C; [C2] by S11, S14–S17; [C3]/[C4] by S11–S13. Migration 0011 (D1).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `architecture-reviewer` (project) ∥ `engineering-paved-path:security` ∥ `sdd-engineering:plan-verifier`; then S25
- Review risk: high — first GitLab writes, untrusted LLM text to GitLab, token scoping in skill import, migration, contracts

## Spec change requests / assumptions (defaults planned; [D])
SCR1 x-next-page · SCR2 budgets vs retries · SCR3 ticket order · SCR4 code-ref drift · SCR5 char unit · SCR6 blank last line · SCR7 A placeholders superseded · **SCR8 AC34 / D29 fallback (CRITICAL-conditional)** · A1–A12
