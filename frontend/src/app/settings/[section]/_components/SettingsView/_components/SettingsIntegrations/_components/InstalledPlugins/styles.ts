import type { CSSProperties } from "react";

/** Co-located styles for InstalledPlugins (rows inside the Plugin Import card). */
export const s = {
  title: { fontSize: 12.5, fontWeight: 600, color: "var(--text-secondary)", margin: "0 0 2px" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0 } satisfies CSSProperties,
  skeletons: { marginTop: 6 } satisfies CSSProperties,
  empty: {
    display: "flex",
    flexWrap: "wrap",
    gap: "2px 6px",
    marginTop: 6,
    padding: "8px 11px",
    borderRadius: 6,
    background: "var(--bg-surface)",
    fontSize: 11.5,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  emptyTitle: { fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
} as const;
