# 07 — Blast Radius (client)

Server half + API: [`server/specs/07-blast-radius.md`](../../server/specs/07-blast-radius.md).
Design reference: the homework screenshots (Overview → "BLAST RADIUS" card:
stat row, Tree/Graph toggle, collapsible symbol tree, endpoint/cron chips,
"Prior PRs touching these files" accordion).
Status: **in progress** (2026-10-01).

## Goal

The Overview tab of a PR shows a **Blast radius** block: which symbols the diff
declares, who calls them (file:line, linked to GitHub) and which HTTP
endpoints and crons depend on them — read from `GET /pulls/:id/blast`.

## Out of scope

- PR description + demo video (done by a human).
- Computing anything client-side beyond counting/grouping the response; the
  limits (20 callers/symbol, depth 2) live on the server only.

## Acceptance criteria

1. The Overview tab (`_components/OverviewTab/OverviewTab.tsx`) renders a
   `BlastRadiusCard` next to/under the Intent card, data via a new hook in
   `src/lib/hooks/` with a key from `keys.ts` (`prKeys`).
2. Stat row on top: N symbols · N callers · N endpoints · N cron, labels from
   `messages/en/blast.json` (`stat.*`), counts derived from the response.
3. Under each changed symbol: its callers as `file:line`; under them the
   endpoint chips (globe icon) and, **separately styled**, cron chips (clock
   icon, amber). Symbols ordered as the server returns them (rank desc).
4. Each `file:line` is a link to that exact line on GitHub
   (`githubBlobUrl(repoFullName, headSha, file, line)`), opens in a new tab.
5. Symbols collapse/expand like the screenshot tree (chevron, `<> name()`,
   right-aligned "N callers" via `callerCount`); first symbol expanded.
6. Empty state: changed symbols but no callers → `noDownstream` text, never a
   blank card. Degraded → a distinct badge/notice with the human-readable
   reason, plus a **Resync** button calling `POST /repos/:id/resync` (mutation
   in `src/lib/hooks/`, invalidates the blast + index-state queries).
7. Tree / Graph toggle (`view.tree`, `view.graph`): Graph is a lightweight SVG
   (changed symbol → caller names → endpoints, 3 columns, legend), no new
   heavy dependency; `graph.empty` when nothing to draw.
8. **P3** "Prior PRs touching these files" accordion with a count badge, from
   `GET /pulls/:id/history` (PR number, title, author, merged date,
   overlapping files, linked to GitHub); hidden-safe when empty.
9. All visible strings come from `messages/en/blast.json` (add keys there,
   none hardcoded). Component tests (renderWithProviders + mockFetch) cover:
   stats, grouped callers + links, empty state, degraded badge + resync call,
   tree toggle.
