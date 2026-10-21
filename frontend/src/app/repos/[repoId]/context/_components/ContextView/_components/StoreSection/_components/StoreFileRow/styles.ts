import type { CSSProperties } from "react";

/** Co-located styles for StoreFileRow. */
export const s = {
  row: { display: "flex", alignItems: "center", gap: 4, borderRadius: 7 } satisfies CSSProperties,
  rowActive: { background: "var(--bg-hover)" } satisfies CSSProperties,
  select: {
    flex: 1,
    minWidth: 0,
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "7px 10px",
    borderRadius: 7,
    textAlign: "left",
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  selectActive: { color: "var(--text-primary)" } satisfies CSSProperties,
  icon: { flexShrink: 0, color: "var(--text-muted)" } satisfies CSSProperties,
  iconActive: { flexShrink: 0, color: "var(--accent-text)" } satisfies CSSProperties,
  name: { fontSize: 13, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  rename: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 4, padding: "4px 6px" } satisfies CSSProperties,
  input: {
    width: "100%",
    fontSize: 13,
    padding: "5px 8px",
    borderRadius: 6,
    border: "1px solid var(--border-strong)",
    background: "var(--bg-elevated)",
    color: "var(--text-primary)",
    outline: "none",
  } satisfies CSSProperties,
  error: { margin: 0, fontSize: 12, color: "var(--crit)" } satisfies CSSProperties,
  hint: { margin: 0, fontSize: 11, color: "var(--text-muted)" } satisfies CSSProperties,
  menu: { flexShrink: 0 } satisfies CSSProperties,
} as const;
