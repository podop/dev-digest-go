import type { CSSProperties } from "react";

/** Co-located styles for GithubActionCard. */
export const s = {
  card: { display: "flex", flexDirection: "column", gap: 14 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "flex-start", gap: 12 } satisfies CSSProperties,
  headText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  titleRow: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  title: { margin: 0, fontSize: 15, fontWeight: 700 } satisfies CSSProperties,
  count: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  subtitle: { fontSize: 13, color: "var(--text-secondary)", marginTop: 2, lineHeight: 1.45 } satisfies CSSProperties,
  list: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    overflow: "hidden",
    listStyle: "none",
    margin: 0,
    padding: 0,
  } satisfies CSSProperties,
  skeletonRow: { padding: "14px 16px", borderTop: "1px solid var(--border)" } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", padding: "6px 0" } satisfies CSSProperties,
} as const;
