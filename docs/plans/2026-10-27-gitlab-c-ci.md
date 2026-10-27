# Development Plan: GitLab C — GitLab CI export, runner and ingest
Plan: GitLab C · Status: done (2026-10-27; 11b1c81 W1–W3, 05253c9 W4 + fixes, 5f41e63 S26, dacc9d0 race fix, fix rounds c2a8bc2, 1a8bffc, 621ba72, CiTab fix in the close commit; gate arch r1 PASS, security r3 PASS, plan-verify r2 no FAIL after D-entries; S27 stub run done, AC37/AC48 live pending the user; spec D29–D37) · Earlier: approved (user 2026-10-27: SCR1 = recommendation, GitLab ledger key `ci:<host>:<job id>:<attempt>`, GitHub unchanged; SCR2–SCR10, A1–A15 under pre-approval; run autonomously) · Spec: specs/2026-10-24-gitlab-c-ci.md (ready-for-planning, 4819a47; drift in [F], SCR below) · Base: feat/go-backend-phase-1@21a3ce4
Packages: backend, frontend · Context pack: docs/plans/2026-10-27-gitlab-c-ci.context.md
Execution: multi-agent, parallel lanes (user default) · Order: GitLab B lands first; ⏳B = waits for B (S2 ← B S2 0011; S6 ← B S3 `GL/` postOnce/raw GET; S16, S21 ← B S1 `NB/`, committed alone; S20, S26 ← B W3/W5) per `docs/plans/2026-10-27-gitlab-b-comments-context.md`

## Goal
Export target `gitlab_ci` (job file, root include, MR), GitLab rule checker, the runner in GitLab MR pipelines (drafts + bulk publish, ≤ 50 threads, B note-body rules), ingest of GitLab job artifacts, host-aware CI UI. GitHub byte-identical (NFR8).

## Out of scope
Spec §3; B's MR comments; `vendor/ui`; a GitLab Integration card other than FR45.

## Decisions
- P1 Migration **0012** (B owns 0011); tests pinned to fixed sets, `TestMigration0012` up/down/up [K7] (orchestrator)
- P2 Note bodies only via B's stdlib leaf `NB/` [C-NB]; C re-implements nothing (orchestrator); runner closure stays green [F8]
- P3 Installation identity: two partial uniques per target (gha: host+repo, gitlab: host+project_id), slug likewise; ci_runs unique (host, github_run_id, run_attempt) (A5) [F6]
- P4 GitLab checker = `wfrules.CheckGitLab` beside `CheckWith` (shares the bounded loader, tag check on yaml.Node) [F10][K10]
- P5 Server GitLab calls: new files in `GL/` (writes never retried, one credential-free https artifact hop) [C5][K8]
- P6 Runner GitLab client `CR/adapters/glapi` mirrors ghapi; diff via `CR/adapters/gitdiff` (os/exec git, no gitcli) [C8][K2]
- P7 Runner positions/cap/log rules are pure in `CR/domain` [C7]; reviewer kernel untouched [F7]
- P8 Ledger key host-qualified for GitLab (SCR1) [C9][K5]
- P9 B order: B first; ⏳B steps re-read B's API at S0 (orchestrator)

## Steps
Done when = the step's tests via [V1]/[V2] + build/typecheck green; UI + pseudo-messages [K13]. `+t` = with tests.

### S0 Base check [W0, orchestrator]
B landed (0011, `NB/` names → [C-NB]), re-check [F], brief lanes · Covers §12
### S1 Contracts + hooks [W1-a]
M `SH/eval-ci.ts`, `F/lib/hooks/ci.ts`+t, `F/lib/types.ts` [C1] · Covers FR1, FR37, FR46, §7.1
### S2 Migration 0012 ⏳B [W1-b]
A `B/migrations/0012_gitlab_ci.{up,down}.sql`; M `migrate/migrate_test.go` (P1, P3, A12) · Covers FR39, EC30, EC33, AC36
### S3 Job file + MR text [W1-c]
A `CI/domain/{gitlabjob,gitlabmr}.go`+t (golden §7.3, §7.4, SCR3, SCR9) [C2] · Covers FR4, FR6–FR10, FR15, NFR4, AC7, AC9, AC16
### S4 Rule checker [W1-d]
A `wfrules/gitlab.go`+t (EC11–EC13 corpus, fuzz, 64 KiB ≤ 50 ms) [C3] · Covers FR14, NFR5, AC15
### S5 Ingest rules [W1-e]
A `CI/domain/gitlabingest.go`+t [C4] · Covers FR32–FR36, EC27, EC28
### S6 GitLab endpoints ⏳B [W1-f]
A `GL/{ci_publish,ci_ingest,artifact}.go`+t (httptest TLS); M `GL/rest.go` (`CIConfigPath`) [C5] · Covers FR11, FR12, FR34, NFR1
### S7 Runner rules [W1-g]
A `CR/domain/{glenv,glpositions,glcap,gllog}.go`+t (fuzz positions) [C7] · Covers FR16–FR20, FR29, FR30, FR48, AC28
### S8 glapi [W1-h]
A `CR/adapters/glapi/*`+t (A8) [C8] · Covers FR24–FR28, NFR2, NFR7, NFR9
### S9 gitdiff [W1-i]
A `CR/adapters/gitdiff/*`+t (real git fixture, A9) [C8] · Covers FR20, NFR10
### S10 Ledger key [W1-j]
M `SP/domain/ledger.go`+t, `SP/app/recorder.go`+t (SCR1) [C9] · Covers FR36, EC30
### S11 Export app [W2-k]
M `CI/app/{export,api,ports}.go`; A `CI/app/export_gitlab.go`+t (fakes; SCR6) [C6] · Covers FR1–FR5, FR11–FR13, EC1–EC10, AC1–AC6, AC10–AC14
### S12 Ingest app [W2-l]
M `CI/app/ingest.go`; A `CI/app/ingest_gitlab.go`+t (A7) [C6] · Covers FR31–FR36, AC30–AC34
### S13 Store [W2-m]
M `CI/adapters/postgres/*`+t (pgtest, P3, [K6][K10]) · Covers FR13, FR36, FR37, FR39, FR46, AC35
### S14 Peers + GitLab adapter [W2-n]
M `CI/adapters/peers/directories.go`+t; A `CI/adapters/gitlab/*`+t [C6] · Covers FR1, FR5, FR11–FR13, FR32–FR34, NFR6
### S15 Transport [W2-o]
M `CI/transport/http/{dto,routes}.go`+t (SCR10) · Covers FR1, FR37, FR46, §7.1
### S16 Runner GitLab mode ⏳B [W2-p]
M `CR/app/{run,ports,api}.go`; A `CR/app/gitlab.go`+t [C8][C-NB] · Covers FR16–FR28, FR38, FR48, NFR3, AC27
### S17 Wizard [W2-F1]
M `EW/**`, `E/ci.json` +t (SCR4) [C1] · Covers FR40–FR43, AC38–AC40, AC43, AC16 (box)
### S18 CI runs [W2-F2]
M `F/lib/ci-runs.ts`+t, `F/app/ci-runs/_components/CiRunsView/**`+t, `E/runs.json` · Covers FR44, EC34, AC41
### S19 Integrations + copy [W2-F3]
M `SI/{GithubActionCard,InstallActionDialog}/**`+t, SettingsApiKeys + SettingsAutoReviews info, `E/{integrationsGithubAction,apiKeys,automaticReviews}.json` (SCR5) · Covers FR45, FR47, AC42, AC43
### S20 Composition root ⏳B [W3-x]
A `S/ci_gitlab.go`; M `S/{app,ledger}.go`, `B/AGENTS.md` · Covers FR31, NFR1 · Done when `make backend-check` [K12]
### S21 Runner main + closure ⏳B [W3-q]
M `R/main.go`, `R/closure_test.go` (exact `GL/` forbid, require `NB/`, glapi) [F8][K9] · Covers FR16, FR29, NFR2, NFR10
### S22 Export component tests [W4-1]
A `S/gitlab_ci_export_test.go`, goldens `ci_gl_*` · Covers AC1–AC6, AC10–AC14, AC44 (publish)
### S23 Ingest + lists component tests [W4-2]
A `S/gitlab_ci_ingest_test.go`; regenerate CI goldens [K11] · Covers AC3, AC30–AC35, AC44, AC45
### S24 Runner env/diff tests [W4-3]
A `R/gitlab_env_test.go`, `R/gitlab_script_test.go` · Covers AC8, AC17–AC23, AC29
### S25 Runner posting tests [W4-4]
A `R/gitlab_post_test.go` (stub GitLab, mock model) · Covers AC24–AC27, AC46, AC47
### S26 Contract test ⏳B [W5-5]
M `F/test/go-backend-contracts.test.ts` [K11][K13] · Covers NFR8, §7.1
### S27 Manual [W5-6]
Throwaway stack [K15] + checklists · Covers AC37, AC48 (real gitlab.com, A13) · Done when reported

## Execution
- Mode: multi-agent (~110 files, 2 packages, contract + data change)
- W0 S0 · W1 a–j (10) · W2 k–p ∥ F1–F3 (9) · W3 x ∥ q · W4 1–4 · W5 5 ∥ 6
- Before B ends: W1 a, c, d, e, g, h, i, j; W2 k, l, o, F1–F3 (no `NB/`, no B files). After B: S2, S6, S13 (needs S2), S14 (needs S6), S16, W3–W5
- Lanes share no file; Go lanes code against [C2]–[C9], UI against S1; W4 helper prefixes `ge…`/`gi…`/`gr…`/`gp…`

## Contracts & migrations
S1 ↔ S15; goldens S22–S23 → S26. Migration 0012 (P1, P3).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3]
- Review gate: `architecture-reviewer` (project) ∥ `engineering-paved-path:security` ∥ `sdd-engineering:plan-verifier`; then S27
- Review risk: high — outbound tokens + signed URLs, untrusted YAML, runner secrets/job log, quick-action bodies, contracts, migration, ledger

## Spec change requests / assumptions (defaults planned; A1–A15 in [D])
- SCR1 **CRITICAL** (append-only ledger, spend cap): GitLab job ids collide with GitHub run ids in the `ci:<id>:<attempt>` key (EC30) → GitLab key `ci:<host>:<id>:<attempt>`, GitHub unchanged, no migration [F5]
- SCR2 notebody names unknown → [C-NB] until S0 · SCR3 EC14/EC15 warnings static (no root-config parse) after §7.4 step 2 and in the box
- SCR4 AC43 without axe (no dependency): RTL role/name checks · SCR5 FR47 key links Integrations [F11] · SCR6 keep 409 plugin_disabled after agent [F3]
- SCR7 rule 5 `DEVDIGEST_LLM_HEADER_n` only n ≤ headerCount · SCR8 GitLab strip set exactly FR20 · SCR9 image `buildpack-deps:trixie-scm@sha256:<linux/amd64>` pinned in S3 (not verifiable here) · SCR10 `repo` stays required beside `repo_id`
- Overlap with B: `S/{app,gitlab}.go`, `GL/{client,rest}.go`, `migrate_test.go`, migration numbers, `F/test/go-backend-contracts.test.ts`, `F/lib/hooks/{index,keys}.ts`, `F/lib/code-host/*`
