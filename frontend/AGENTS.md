# client — `@devdigest/web`

## Commands (pnpm)
- `pnpm dev` (:3000, needs the API on `NEXT_PUBLIC_API_BASE`; Go backend in compose: :3101) · `pnpm test` (vitest + jsdom) · `pnpm typecheck` · `pnpm lint`

## Read when
- Adding a page or wiring a screen to the API → read `README.md` (UI route map)
- Changing a real user journey → also read `../e2e/README.md` (flows may need updating)
- Implementing a planned feature → look for its spec in `specs/`
- Start of every task here → read `INSIGHTS.md` first; at the end → `engineering-insights` wrap-up

## Conventions (non-default)
- Pages (`src/app/**/page.tsx`) stay thin; feature logic lives in colocated
  `_components/<Name>/` folders, each with its own `*.test.tsx`.
- Data only through TanStack Query hooks in `src/lib/hooks/*` → `src/lib/api.ts`.
  No direct `fetch` in components.
- Every user-facing string goes to `messages/en/*.json` (next-intl).
- Shared chrome (nav, breadcrumbs, `g`-then-key shortcuts) is in `src/components/app-shell`.
- Component tests mock `fetch`; they never need the API or a browser.

## Gotchas
- `src/vendor/shared` is the wire contract the Go backend (`../backend`) must honor
  (copied from dev-digest `server/src/vendor/shared` at `d1c9093`). Change a contract
  only together with the matching backend change.
- Runtime values (schemas) may be imported from
  `@devdigest/shared`: `next.config.mjs` maps its `./x.js` specifiers to `.ts`.
  The barrel pulls zod into the route; take `FEATURE_MODELS` from the zod-free
  `@devdigest/shared/constants/feature-models`.
- API base comes from `NEXT_PUBLIC_API_BASE` (default `http://localhost:3001`); the Docker
  image bakes it at build time (compose passes `http://localhost:${API_PORT:-3101}`).

## Do not touch
- `src/vendor/ui` (`@devdigest/ui`) — vendored primitives; compose, don't edit. Local exceptions (keep them on
  any re-sync): registration lists `icons.tsx`/`nav.ts` (9ef3155) and the additive `onClick` prop of
  `AutoTriggerStatus.tsx` (0722f1a, Settings C D18), the additive `itemFontSize` prop of `kit/Dropdown.tsx`
  (default 14; the repo switcher passes 11, user 2026-10-29), and the compact sizes of `shell/Sidebar.tsx`, `NavItem.tsx`,
  `RepoSwitcher.tsx`, `Topbar.tsx` (prototype chrome.jsx sizes, user 2026-10-27), and `RepoSwitcher.tsx` showing
  "Add repository…" only when `ctx.onAddRepo` is provided (the app passes none; repositories are added in Settings ›
  First-run setup; user 2026-10-27).
- Lint = Biome, linter only, config `biome.jsonc` here (copied from dev-digest's root; must stay
  `.jsonc`, `vcs.root` points at the repo root `.gitignore`). Never run `biome check --write`,
  `biome format` or enable the formatter — it would rewrite the whole package. Rules set to
  `info` are known debt: fix their hits, then delete the line.
