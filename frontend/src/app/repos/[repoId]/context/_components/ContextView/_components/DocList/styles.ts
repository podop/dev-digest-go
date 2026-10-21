import type { CSSProperties } from "react";

/** Co-located styles for DocList. */
export const s = {
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 2 } satisfies CSSProperties,
  rowBase: {
    width: "100%",
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "7px 10px",
    borderRadius: 7,
    textAlign: "left",
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  rowActive: { background: "var(--bg-hover)", color: "var(--text-primary)" } satisfies CSSProperties,
  icon: { flexShrink: 0, color: "var(--text-muted)" } satisfies CSSProperties,
  iconActive: { flexShrink: 0, color: "var(--accent-text)" } satisfies CSSProperties,
  text: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column" } satisfies CSSProperties,
  name: { fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  folder: {
    fontSize: 11,
    color: "var(--text-muted)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  badge: { flexShrink: 0 } satisfies CSSProperties,
} as const;
