import type { CSSProperties } from "react";

/** Co-located styles for the PR Leaderboard page shell. */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 1240, margin: "0 auto", display: "flex", flexDirection: "column", gap: 22 } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  skeletonTiles: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  skeletonTile: { flex: "1 1 180px", minWidth: 160 } satisfies CSSProperties,
} as const;
