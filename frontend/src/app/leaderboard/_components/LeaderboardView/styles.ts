import type { CSSProperties } from "react";

/** Co-located styles for the PR Leaderboard page shell. */
export const s = {
  /** Full width with a small gutter (user, 2026-10-27: less padding left and right of the content). */
  page: { padding: "12px 16px 10px", margin: "0 auto", display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  loaded: { display: "flex", flexDirection: "column", gap: 8, minWidth: 0 } satisfies CSSProperties,
  /** A row that wraps into a column when its children no longer fit side by side. */
  main: { display: "flex", flexWrap: "wrap", gap: 8, alignItems: "flex-start" } satisfies CSSProperties,
  /** The table takes every pixel the side column leaves. */
  mainWide: { flex: "1 1 720px", minWidth: 0 } satisfies CSSProperties,
  mainSide: { flex: "0 1 360px", minWidth: 280, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  half: { flex: "1 1 340px", minWidth: 0 } satisfies CSSProperties,
  tableEmpty: { border: "1px solid var(--border)", borderRadius: 10, background: "var(--bg-elevated)", overflow: "hidden" } satisfies CSSProperties,
  skeletonTiles: { display: "flex", gap: 8 } satisfies CSSProperties,
  skeletonTile: { flex: "1 1 0", minWidth: 0 } satisfies CSSProperties,
} as const;
