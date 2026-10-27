# Context pack: GitLab B
[A] `B/`=backend, `S/`=B/cmd/server; in B/internal `NB/`=platform/gitlab/notebody, `GL/`=platform/gitlab, `PU/`=pulls, `BL/`=blast, `BR/`=brief, `IN/`=intent, `SK/`=skills, `SD/`=smartdiff, `MG/`=platform/postgres/migrate · `F/`=frontend/src, `SH/`=F/vendor/shared, `PR/`=F/app/repos/[repoId]/pulls/[number]/_components, `DV/`=F/components/diff-viewer, `SG/`=F/app/settings/[section]/…/SettingsGitlab, `E/`=frontend/messages/en

## Lane contracts
- [C1] `NB` (stdlib, frozen for C): `const MaxChars = 1_000_000`; `const ZWSP = "\u200b"`; `func Apply(body string) string` (FR14→15→16, whole body, linear, idempotent); `func Chars(s string) int` (code points, SCR5). Corpus `NB/testdata/ec15.json` `[{name,in,want}]` (stable path)
- [C2] `GL` (all `(ctx, conn, pid, iid, …)`): `MergeRequestDetail.DiffRefs{BaseSHA,StartSHA,HeadSHA}`; `File.OldPath`; `Versions`; `DiscussionsPage(…,page,perPage)` → `Discussion{ID,Notes}`, `Note{ID int64,Type,System,Body,Author,CreatedAt,Position}`, `Position{Type,Base/Start/HeadSHA,Old/NewPath,Old/NewLine *int}`; `CreateDiscussion(…,body,Position)`, `CreateNote(…,discID,body)` one attempt; `MergedMergeRequests(ctx,conn,pid,perPage)`; `DiffPaths` (page 1); `RawFile(ctx,conn,project,file,ref,max)` (`ErrTooLarge`); `StatusError.RetryAfterSeconds/InsufficientScope()`; `Binding{ID,Name,BaseURL,Host,Token}.Conn()`
- [C3] `PU/domain`: `Repo.WebURL`, `Pull.DiffHead`, `File.PreviousPath`, `Fetched/DetailMirror.DiffHead` (*string); `Discussion/Note/Position`, `DiffRefs.Complete()`; `MapDiscussions(ds,diffHead,webURL,n)` (FR2–FR4); `PositionFor(f,side,line) (old,new *int, reason)` (FR6); `GitLabFailure{…}.Classify() (code,reason)` (FR10)
- [C4] `PU/app`: port `GitLabComments{Ready; Discussions(…,page) (ds,more,err); MergeRequestRefs; LatestVersion; CreateDiscussion; CreateNote}`; errors `ErrNotConnected`, `*NoTokenError`, `*GitLabError`(+RetryAfterSeconds, InsufficientScope), `ErrGitLabRedirect`, `ErrGitLabUnavailable`; `WithGitLabComments`, `WithRedact`
- [C5] `GL.ResolveFunc(ctx,hostKey) (Binding,bool,error)`, `GL.ListFunc(ctx)`; ports BL `GitLabHistory.ListMerged`, BR `GitLabIssues.Issue`, IN `GitLabReads{Issue,RawFile}`, SK `gitLabFiles{Match,Raw}`; cmd binds over `gitlabconnapp.Service`
- [C6] UI: `sanitizeForGitLab` = `sanitizeForGitHub` + FR15 + FR16(c); `findingCommentBody/Input(f,{gitlab})`; `useCreatePrComment(prId,{gitlab})` quiet, onError refetches `prKeys.detail` on `diff_changed|diff_not_ready`; `commentErrorMessage`; `DiffCommentApi.host`; `publish.gitlab`

## Repo rules and notes
- [K1] `CLAUDE.md:38-45` wire compat, guards; `:93` contracts · [K2] `B/AGENTS.md:60-73` layout, ports; `:231-240` edges, apperr
- [K3] `B/INSIGHTS.md:89` "redact … before truncating"; A D42 · [K4] `B/AGENTS.md:251-256` Conn in InTx; `B/INSIGHTS.md:67` DoUpdates
- [K5] `B/INSIGHTS.md:150` "pin each migration test to a fixed set … TestMigration000N up/down/up"; `:68`; `B/AGENTS.md:241`
- [K6] `B/INSIGHTS.md:36`, `:26` · [K7] `:52` fake DNS, `:99` SSL_CERT_FILE, `:143` exact log counts, `:146` 25m
- [K8] `:64` golden + contract entry; `:54` `-run Contracts -update` · [K10] `:53` throwaway volumes · [K11] `:23` loops not regexp; `:163` U+200B escapes via python · [K12] `:148` flakes
- [K9] `frontend/AGENTS.md:13-19`; `F/INSIGHTS.md` :48, :91, :56 quiet codes, :62 keys, :31 useLinkRepo, :38 toasts, :107-108

## Verified facts
- [F1] `PU/app/service.go:286-345` A stubs (spec cites :238-293)
- [F2] `GL/client.go:235-248` all retry; `:356-382` HTML → status text; `:66-76` RetryAfter time · [F3] `:8-9` no X-Next-Page · [F4] `GL/rest.go:177-196` no diff_refs, `:309-341` no OldPath
- [F5] `PU/adapters/postgres/store.go:29` has WebURL, `PU/domain/pulls.go:44-55` not
- [F6] FR33 links done by A (`PriorPrs.tsx:64-74`, `IntentCard/helpers.ts:32-35`, tested); `E/skills.json:79` left
- [F7] `Classify` used by `SD/domain/build.go:37`, `BR/adapters/peers/peers.go:151`; SD store has no provider
- [F8] `SK/domain/urlhost.go:304-356` refuses port ≠ 443, private IP
- [F9] `cmd/devdigest-runner/closure_test.go:14-30` prefix denylist; NB in none (C: never deny prefix `…/platform/gitlab`)
- [F10] A stubs `BL/app/service.go:90`, `BR/app/facts.go:82`, `IN/adapters/peers/tickets.go:55`, `docs.go:78`; `IN/domain/links.go:112-189` pattern order
- [F11] A placeholders in `S/gitlab_isolation_test.go:443-550`, `PU/app/service_gitlab_test.go`, `IN/adapters/peers/peers_test.go`, `PR/DiffTab/DiffTab.test.tsx`
- Mirrors: `platform/github/{comments,merged}.go`, `0007`

## Details [D]
- D1 0011: `pull_requests.diff_head text` CHECK `^[0-9a-f]{40}([0-9a-f]{24})?$`, `pr_files.previous_path text`; down drops both
- SCR1 x-next-page → page numbers ≤ 3 · SCR2 budgets = logical requests · SCR3 GitLab tickets by text position (EC20), GitHub as is · SCR4 line drift, FR33 mostly done · SCR5 chars = code points · SCR6 blank = spaces/tabs · SCR7 A placeholders replaced · SCR8 CRITICAL-conditional AC34/D29
- A1 `rejected` ≤ 300 runes · A2 versions `per_page=1` · A3 disc id 8–64 hex · A4 JSON POST · A5 date → ceil s; absent/0 → 60 · A6 `insufficient_scope` = message · A7 first `/-/blob/` · A8 null merged_at → "" · A9 AC14 `!race` · A10 P13 · A11 `isNotConnected` · A12 identity from test results

## Skill map (`pp:` = `engineering-paved-path:`)
| Steps | Skills | Rules |
|---|---|---|
| S1–S7, S11–S23 Go | go-architecture, go-testing | std domain, ports; fakes, TLS stubs, fuzz |
| S2, S12 | pp:postgresql-table-design | nullable text + CHECK |
| S1, S3, S11, S13, S17, S21 | pp:security | no echo, token scope, fail closed |
| S8–S10, S18, S24 | pp:react-best-practices, -testing-library, pp:typescript-expert | derive, one toast |

## Verification sources
[V1] `env -u GOROOT PATH="$HOME/.local/go/bin:$PATH" go test -race -count=1 <pkgs>` (`B/AGENTS.md:9-11`) · [V2] `pnpm exec vitest run <fragment>`, `pnpm typecheck`, `pnpm exec biome lint --error-on-warnings <dir>` (`frontend/AGENTS.md:4`, `F/INSIGHTS.md:72`) · [V3] `make backend-check|frontend-check|check` (`Makefile:107-149`)
