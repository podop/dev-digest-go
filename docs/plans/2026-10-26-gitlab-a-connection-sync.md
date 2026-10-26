# Development Plan: GitLab A — connections and MR sync
Plan: GitLab A · Status: approved (user 2026-10-26: SCR1, SCR2, SCR5 answered as planned; SCR3, SCR4, SCR6, SCR7, A1–A13 under pre-approval; spec D36–D37; migration 0010; run autonomously) · Spec: specs/2026-10-24-gitlab-a-connection-sync.md (reconciled at 13aaf62) · Base: feat/go-backend-phase-1@13aaf62
Packages: backend, frontend · Context pack: docs/plans/2026-10-26-gitlab-a-connection-sync.context.md
Execution: multi-agent, parallel lanes (user default)

## Goal
Global GitLab connections; GitLab projects next to GitHub repos, bound by host key; MRs feed PR list, leaderboard, auto reviews; host-aware links; read-only.

## Out of scope
Spec §3; slices B (comments, peers) and C (CI); `vendor/ui`.

## Decisions
- P1 New context `GC/`, reached via cmd ports [K2][C4]
- P2 Pure rules: `CH/` stdlib; base URL = `custom.CheckBaseURL` + https only in `GC/domain` [C1][C3][F3]
- P3 `GL/` on `EG.Client` (no redirect/proxy, SCR2); token per call; page numbers, never `Link` URLs [K7][C7]
- P4 Binding by host key at read time: repos/leaderboard SQL LEFT JOIN `gitlab_connections` (D26); no FK
- P5 Clone key `@gitlab/<host>[_<port>]/<project id>`; every clone reader passes `CH.CloneKey` (A1) [F1][C6]
- P6 GitLab git: `GI.Remote` per command (scoped Basic `oauth2`, no redirects, CA bundle SCR5, resolve pin, no proxy); GitHub unchanged (A2) [F2]
- P7 Token `gitlab_connections.<id>` in `SE/`, written in the DB tx, restored when it does not commit [K5][C5]
- P8 0010: table, repos provider/host/project_id/web_url, new uniques; down refuses on GitLab data (A11) [K9][F4]
- P9 Poller: per-host pause replaces `pausedUntil`/`holdUntil`; D17 kept [K14][F5]
- P10 MR `head_is_fork` = source ≠ target project id, missing → fork (SCR1) [K15]
- P11 Stats: GraphQL `projects(ids:[gid])`, ≤ 20 iids, query only (SCR4) [K14]
- P12 FR29: each consumer reads the provider with its row [F7]
- P13 Limit 20 via advisory lock; host 23505 → 409 [K10]
- P14 UI: `F/lib/code-host` for links, `!n`, labels; switcher via `toShellRepo` [C9][F10]

## Steps
Done when = the step's tests via [V1]/[V2] + build/typecheck green; UI steps + pseudo-messages check [K13]. `+t` = with tests.

### S0 Base check [W0, orchestrator]
`0010` free, re-check [F], brief lanes · Covers spec §12
### S1 Contracts + hooks [W1-a]
M `SH/contracts/{platform,leaderboard}.ts`, `F/lib/{api,types}.ts`, `F/lib/hooks/{core,keys,index}.ts`; A `F/lib/hooks/gitlab-connections.ts`+t, `F/lib/code-host/`+t [C9] · Covers FR12, FR16, FR41, FR46
### S2 Migration 0010 [W1-b]
A `B/migrations/0010_gitlab.{up,down}.sql`; M `migrate/migrate_test.go` (P8) · Covers FR30, EC29, AC12
### S3 Code-host rules [W1-c]
A `CH/*`+t; M `RP/domain/repo.go`+t (fuzz `ParseURL`) [C1][C2] · Covers FR12, FR13, AC10
### S4 Connection domain [W1-d]
A `GC/domain/*`+t (fuzz base URL) [C3] · Covers FR1, FR2, FR11, AC2, AC7
### S5 Secrets + redaction [W1-e]
M `SE/secrets.go`+t, `onboarding/domain/redact.go`+t [C5][K3] · Covers FR4, FR31, NFR1
### S6 gitcli [W1-f]
M `GI/{paths,gitcli}.go`+t; A `GI/cabundle.go`+t [C6] · Covers FR18–FR20, AC14
### S7 GitLab client [W1-g]
A `GL/*`+t (httptest TLS, synctest timeouts) [C7] · Covers FR10, FR14, FR21–FR26, NFR2–NFR5, NFR9
### S8 Pulls domain + ports [W1-h]
M `PU/domain/{pulls,poll}.go`+t; A `PU/app/gitlab_ports.go` [C8] · Covers FR22, FR24, FR27
### S9 GC app [W2-i]
A `GC/app/*`+t (fakes) · Covers FR3–FR11, AC3, AC4, AC6
### S10 GC store [W2-j]
A `GC/adapters/postgres/*`+t (pgtest) [K10][K6] · Covers FR3, FR7, FR8, NFR8
### S11 GC routes [W2-k]
A `GC/transport/http/*`+t [K4] · Covers FR3, FR4, FR7, NFR6, AC1, AC3
### S12 Settings test [W2-l]
M `ST/transport/http/{validate,handler}.go`+t, `ST/app/settings.go`+t · Covers FR9, FR10
### S13 Repos app [W2-m]
M `RP/app/{service,jobs}.go`+t · Covers FR13–FR15, FR47, FR48, AC9, AC11
### S14 Repos store + routes [W2-n]
M `RP/adapters/postgres/store.go`+t, `RP/transport/http/handler.go`+t · Covers FR15, FR16, AC5
### S15 Pulls service [W2-o]
M `PU/app/{service,ports}.go`+t · Covers FR21, FR23–FR25, FR29, FR48
### S16 Poller [W2-p]
M `PU/app/poller.go`+t (synctest) · Covers FR27, AC19, AC38
### S17 Pulls adapters [W2-q]
A `PU/adapters/gitlab/*`+t; M `PU/adapters/postgres/store.go`+t · Covers FR17, FR47
### S18 Clone readers I [W2-r]
M `repointel/{domain,adapters/postgres,app}` repo reads + git calls +t · Covers FR18
### S19 Clone readers II [W2-s]
M `reviews/adapters/{postgres/reads.go,peers/projectcontext.go}`, `onboarding/adapters/{postgres,peers}` +t · Covers FR18
### S20 GitHub-only consumers [W2-t]
M `intent/adapters/{postgres,peers}`, `brief/{adapters/postgres,app/facts.go}`, `blast/{adapters/postgres,app/service.go}` +t · Covers FR29
### S21 Auto reviews [W2-u]
M `AR/domain/status.go`+t, `AR/app/{ports,status}.go`+t · Covers FR28
### S22 Leaderboard [W2-v]
M `LB/adapters/postgres/queries.go`, `LB/transport/http/dto.go` +t · Covers FR46
### S23 CI export [W2-w]
M `ci/adapters/peers/directories.go`, `ci/app/{export,ports}.go` +t · Covers FR29
### S24 GitLab Integration [W2-F1]
A `SV/SettingsGitlab/**`, `E/gitlabIntegration.json`; M `SettingsView/{constants.ts,SettingsView.tsx}` +t [K16] · Covers FR32–FR36, AC27, AC28, AC35, AC42
### S25 GitHub Integration [W2-F2]
M `SV/SettingsGithub/_components/{ConnectedRepos,RemoveRepoDialog}/**`+t · Covers FR37, AC29
### S26 Setup + home [W2-F3]
M `SetupView/**` (GithubStep → CodeHostStep), `HomeView/**`, `E/{setup,home}.json` +t [K17] · Covers FR38, FR39, AC30
### S27 Add repo [W2-F4]
M `F/components/add-repo-form/**`, `E/addRepo.json` +t · Covers FR40, AC31
### S28 PR list + detail [W2-F5]
M `PullsView/**`, `PrDetailHeader/**`, `PrDetailView/**`, `pulls/[number]/page.tsx`, `E/prReview.json` +t · Covers FR42, FR43, FR49, AC32, AC42
### S29 Links + posting [W2-F6]
M `F/lib/github-urls.ts` callers [F10], `FindingCard`, `InlineFindingCard`, `DiffTab` +t · Covers FR41, FR42, FR45, AC32, AC34
### S30 Lists + labels [W2-F7]
M `leaderboard/_components/**`, `RunHistory`, `F/components/app-shell/helpers.ts`, `E/{leaderboardPanels,leaderboardTable,runs}.json` +t · Covers FR43, FR44, AC33
### S31 Auto-review copy + CI pickers [W2-F8]
M `AutoReviewStatus/**`, `E/autoReviewStatus.json`, `ExportWizard/useExportWizard.ts`, `InstallActionDialog/**` +t · SCR3 · Covers FR28
### S32 Composition root [W3-x]
A `S/gitlab.go`; M `S/{app,autoreview}.go`, `.env.example`, `B/AGENTS.md` · Covers FR6, FR20, FR31 · Done when `make backend-check` [K12]
### S33 Connection component tests [W4-1]
A `S/gitlab_conn_test.go`, goldens `gitlab_conn_*` · Covers AC1–AC8, AC26
### S34 Repo + clone component tests [W4-2]
A `S/gitlab_repos_test.go`, `S/gitlab_clone_test.go` (TLS `git http-backend` stub) · Covers AC9–AC14, AC39, AC40
### S35 Sync component tests [W4-3]
A `S/gitlab_sync_test.go`, `S/gitlab_poll_test.go` [K11][K14] · Covers AC15–AC21, AC38, AC40, AC41
### S36 Isolation component tests [W4-4]
A `S/gitlab_isolation_test.go` · Covers AC22–AC24, AC37
### S37 Contract test [W5-5]
M `F/test/go-backend-contracts.test.ts`; review golden diff [K11] · Covers NFR7, AC24
### S38 Manual [W5-6]
Throwaway stack [K18] · Covers AC25, AC36 (needs user's token, A13) · Done when reported

## Execution
- Mode: multi-agent (~130 files)
- W0 S0 · W1 a–h · W2 i–w ∥ F1–F8 (23 lanes) · W3 x · W4 1 ∥ 2 ∥ 3 ∥ 4 · W5 5 ∥ 6
- Lanes share no file; Go lanes code against [C1]–[C8], UI against S1 [C9]; W4 helper prefixes `gc…`/`gr…`/`gs…`/`gi…`
- Commit W1–W3 as one change set [K1]; W4, W5 each

## Contracts & migrations
S1 ↔ S11–S14, S21, S22; goldens S33–S36 → S37. Migration 0010 (P8).

## Verification
- Gates: `make backend-check`, `make frontend-check` per wave; `make check` last [V3] (`cmd/server` `-timeout 25m` [K12])
- Review gate: `architecture-reviewer` (project) ∥ `engineering-paved-path:security` ∥ `sdd-engineering:plan-verifier`; then S38
- Review risk: high — outbound tokens, SSRF/CA, git auth, contracts, migration, poller

## Spec change requests / assumptions (defaults planned; [D])
SCR1 MR fork flag (security) · SCR2 no proxy, git pin · SCR3 CI pickers · SCR4 GraphQL gid · SCR5 git CA · SCR6 lb `repo` · SCR7 paths · A1–A13
