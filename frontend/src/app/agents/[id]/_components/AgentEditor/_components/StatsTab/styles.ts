import type { CSSProperties } from "react";

/** Co-located styles for the agent Stats tab. */
export const s = {
  wrap: { maxWidth: 1240, display: "flex", flexDirection: "column", gap: 20 } satisfies CSSProperties,
  header: { display: "flex", justifyContent: "flex-end" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  cards: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  skeletonCard: { flex: "1 1 170px", minWidth: 160 } satisfies CSSProperties,
  grid: { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(340px, 1fr))", gap: 16 } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  marker: { color: "var(--warn)", cursor: "help" } satisfies CSSProperties,
} as const;
