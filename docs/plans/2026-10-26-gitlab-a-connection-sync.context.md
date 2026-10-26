# Context pack: GitLab A
[A] `B/`=backend, `S/`=B/cmd/server; in B/internal `GC/`=gitlabconn, `GL/`=platform/gitlab, `CH/`=platform/codehost, `GI/`=platform/gitcli, `EG/`=platform/egress, `SE/`=platform/secrets, `RP/`=repos, `PU/`=pulls, `AR/`=autoreview, `LB/`=leaderboard, `ST/`=settings · `F/`=frontend/src, `SH/`=F/vendor/shared, `SV/`=F/app/settings/[section]/_components/SettingsView/_components, `E/`=frontend/messages/en

## Lane contracts
- [C1] `CH/` stdlib: `GitHub`, `GitLab`, `GitHubHost`; `HostKey(host, port)`; `CloneKey(prov, host, id, owner, name)` → GitHub `(owner, name)`, GitLab `("@gitlab/"+host(':'→'_'), "<id>")`; `WebURL(base, full)`
- [C2] `RP/domain.ParseURL` → `Parsed{Provider, Host, SSH, Path, Owner, Name}`; GitHub unchanged
- [C3] `GC/domain`: `Connection`, `Limit`=20, `CheckBaseURL(raw) (norm, host, msg)` on `custom.CheckBaseURL` + https/`api/v4`/github rules, `CheckName`, `CheckToken`, `Evaluate` (FR11 order); `ErrExists/ErrLimit/ErrNotFound`
- [C4] `GC/app.Service`: List, Create, Update (token omit/null/string), Delete → disconnected, TestConnection(ctx, id, key), `Resolve(ctx, host) (Conn{ID, Name, BaseURL, Host, Token}, ok, err)`, `All`
- [C5] `SE/`: `GitLab`, `SetGitLab`, `DeleteGitLab`, `RestoreGitLab`, `GitLabValues`; key `gitlab_connections.<id>` = `{"token":…}`; no env
- [C6] `GI/`: `ClonePath` accepts the GitLab key (3 levels, symlink check per level); `Remote{URL, Prefix, Token, CAFile}`; `CloneRemote/SyncRemote/DefaultBranchRemote` (origin := URL); `WriteCABundle(extra, dest)`
- [C7] `GL/`: `New(Options{HTTP, Retry, Log})`, `Conn{BaseURL, Token}`, User, TokenSelf, Metadata, ProjectByPath, Project, MergeRequestsPage, MergeRequest, Diffs (≤ 3,000), Commits (≤ 250), Issue, DiffStats; `*StatusError{Status, RetryAfter, Reset}`, `ErrRedirect`, `ErrNotAPI`
- [C8] `PU/`: `domain.Repo` += Provider, Host, ProjectID; `app/gitlab_ports.go`: port (Project, ListPage, Get, Issue, Stats), `ErrNotConnected`, `*NoTokenError{Conn}`, `*GitLabError`; store `UpdateRepoPath` (false on collision)
- [C9] `F/lib/code-host`: `isGitLab`, `isNotConnected`, `prRef` (`!n`/`#n`), `prUrl/issueUrl/blobUrl` from `web_url`, `repoLabel`; hooks `useGitLabConnections`, `useSave…`, `useDelete…`

## Repo rules and notes
- [K1] `CLAUDE.md:38-46` wire compat + guards; `:92` contracts; `:90` no `down -v`
- [K2] `B/AGENTS.md:60-73` layout, cmd ports · [K3] `B/INSIGHTS.md:85` redact then cut; `:142` fixed 401 text
- [K4] `B/AGENTS.md:212-214` `ReadJSONBody`, guards · [K5] `:182-184` secrets in tx; `:205-211` up+down, apperr, git env
- [K6] `:215-221` `postgres.Conn` in `InTx`, pgtest · [K7] `:176-181` egress + CA; `B/INSIGHTS.md:26`, `:36` no redirects; `:48` fake DNS
- [K9] `:141`, `:64` migration tests · [K10] `:84` 23505; `:82` savepoint · [K11] `:50`, `:46`, `:60` goldens + UI entry · [K12] `:137` timeout
- [K13] `frontend/AGENTS.md:13-19`; `F/INSIGHTS.md` :60, :54, :68, :69, :72, :73, :104, :110, :113
- [K14] `B/AGENTS.md:45-47`; `B/INSIGHTS.md:53` due, `:102`, `:61`, `:143`, `:17`
- [K15] `B/AGENTS.md:133-137`; `B/INSIGHTS.md:54`, `:55`
- [K16] `F/INSIGHTS.md` :30, :31, :32, :20, :78, :71 · [K17] :11, :33 · [K18] `B/INSIGHTS.md:49`

## Verified facts
- [F1] clones by (owner,name) `GI/paths.go:54`; readers `repointel/app/{full.go:35,incremental.go:36}`, `projectcontext/app/runcontext.go:82,167` ← `reviews/adapters/peers/projectcontext.go:32`, `onboarding/adapters/peers/blobs.go:31`, `intent/adapters/peers/docs.go:62`, `RP/app/jobs.go:108,155`
- [F2] `GI/gitcli.go:513-530` one token, `https://github.com/` prefix; jobs retry only `retry.IsTransient` (`retry.go:108`)
- [F3] `llm/custom/baseurl.go:26`; `EG/egress.go:128`; CA `S/app.go:229`, `S/customproviders.go:41`; not git
- [F4] `0001:1357`; `RP/adapters/postgres/store.go:71`; seed `store.go:305`; tests need DEFAULTs
- [F5] `PU/app/ports.go:58-91`; `poller.go:72-73`, `:208-211`; DoUpdates `PU/adapters/postgres/store.go:223`
- [F6] `AR/app/ports.go:83`, `S/autoreview.go:122`, `AR/domain/status.go:36`, `AR/adapters/postgres/store.go:49,58`
- [F7] `blast/app/service.go:96`, `brief/app/facts.go:82`, `intent/app/ports.go:67-83`, `PU/app/service.go:243,269`, `ci/adapters/peers/directories.go:113`
- [F8] `ST/transport/http/handler.go:137`, `ST/app/settings.go:136`, `validate_test.go:326`; `S/app.go:424`; `onboarding/domain/redact.go:206`
- [F9] `LB/adapters/postgres/queries.go:31,36`
- [F10] `F/lib/github-urls.ts` (spec §2); `F/components/add-repo-form/`; `F/components/app-shell/helpers.ts:23`; `HomeView/helpers.ts:13`; `SetupView/constants.ts:3`; `SettingsView/constants.ts:2`; `useExportWizard.ts:61`; `SH/contracts/platform.ts:93,154,356`; `RunHistory.tsx:46`
- Mirrors: `customproviders/`, `platform/github`, `0009`

## Details [D]
- SCR1 MR `head_is_fork` = source ≠ target project, null → fork · SCR2 no proxy; git `curloptResolve` pin · SCR3 CI pickers GitHub-only · SCR4 GraphQL `projects(ids:)` · SCR5 git CA = `<DataDir>/git-ca-bundle.pem`; no system bundle → no start · SCR6 lb `repo` = name · SCR7 moved paths
- A1 key [C1] · A2 GitHub git as is · A3 uuid ids · A4 429 w/o hints → next due · A5 1 WARN · A6 export labels · A7 reset as is · A8 no `sha` skip · A9 `FromGitHub` · A10 NULL web_url · A11 down refuses · A12 FR13 · A13 AC36

## Skill map (`pp:` = `engineering-paved-path:`)
| Steps | Skills | Rules |
|---|---|---|
| S2–S23, S32–S36 | go-architecture, go-testing | stdlib domain, ports; fakes, pgtest, synctest |
| S2, S10, S14 | pp:postgresql-table-design | NOT NULL + CHECK, partial unique, host index |
| S4–S7, S11–S13, S32 | pp:security | SSRF, token scope |
| S1, S24–S31, S37 | pp:react-best-practices, pp:react-testing-library, pp:typescript-expert | derive, roles |

## Verification sources
[V1] `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9`) · [V2] `pnpm exec vitest run <fragment>`, `pnpm typecheck`, `pnpm exec biome lint --error-on-warnings <dir>` (`frontend/AGENTS.md:4`, `F/INSIGHTS.md:69,73`) · [V3] `make backend-check|frontend-check|check` (`Makefile:119,144,149`)
