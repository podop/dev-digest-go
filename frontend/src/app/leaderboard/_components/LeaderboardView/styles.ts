import type { CSSProperties } from "react";

/** Co-located styles for the PR Leaderboard page shell. */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1240, margin: "0 auto", display: "flex", flexDirection: "column", gap: 22 } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  loaded: { display: "flex", flexDirection: "column", gap: 18, minWidth: 0 } satisfies CSSProperties,
  /** A row that wraps into a column when its children no longer fit side by side. */
  main: { display: "flex", flexWrap: "wrap", gap: 18, alignItems: "flex-start" } satisfies CSSProperties,
  mainWide: { flex: "2 1 560px", minWidth: 0 } satisfies CSSProperties,
  mainSide: { flex: "1 1 320px", minWidth: 0, display: "flex", flexDirection: "column", gap: 18 } satisfies CSSProperties,
  half: { flex: "1 1 340px", minWidth: 0 } satisfies CSSProperties,
  tableEmpty: { border: "1px solid var(--border)", borderRadius: 10, background: "var(--bg-elevated)", overflow: "hidden" } satisfies CSSProperties,
  skeletonTiles: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  skeletonTile: { flex: "1 1 180px", minWidth: 160 } satisfies CSSProperties,
} as const;
