import type { CSSProperties } from "react";

/** Co-located styles for InstalledPlugins. */
export const s = {
  head: { display: "flex", alignItems: "center", gap: 10, marginBottom: 12 } satisfies CSSProperties,
  title: { fontSize: 15, fontWeight: 700, flex: 1 } satisfies CSSProperties,
  list: {
    border: "1px solid var(--border)",
    borderRadius: 10,
    overflow: "hidden",
    listStyle: "none",
    margin: 0,
    padding: 0,
  } satisfies CSSProperties,
  skeletons: {
    display: "flex",
    flexDirection: "column",
    gap: 10,
    border: "1px solid var(--border)",
    borderRadius: 10,
    padding: 16,
  } satisfies CSSProperties,
  empty: {
    border: "1px dashed var(--border)",
    borderRadius: 10,
    padding: "18px 16px",
    textAlign: "center",
  } satisfies CSSProperties,
  emptyTitle: { fontSize: 14, fontWeight: 600 } satisfies CSSProperties,
  emptyHint: { fontSize: 13, color: "var(--text-muted)", marginTop: 4 } satisfies CSSProperties,
} as const;
