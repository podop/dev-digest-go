import type { CSSProperties } from "react";

/** Co-located styles for ContextPicker. */
export const s = {
  header: { display: "flex", alignItems: "center", gap: 12, marginBottom: 12, flexWrap: "wrap" } satisfies CSSProperties,
  h2: { fontSize: 18, fontWeight: 700, margin: 0 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  filter: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "7px 12px",
    borderRadius: 7,
    border: "1px solid var(--border)",
    background: "var(--bg-surface)",
    width: 260,
  } satisfies CSSProperties,
  filterIcon: { color: "var(--text-muted)" } satisfies CSSProperties,
  filterInput: {
    flex: 1,
    minWidth: 0,
    fontSize: 13,
    background: "transparent",
    border: "none",
    outline: "none",
    color: "var(--text-primary)",
  } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: "0 0 14px" } satisfies CSSProperties,
  list: { padding: 0, margin: "0 0 8px", display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
} as const;
