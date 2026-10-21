import type { CSSProperties } from "react";

/** Co-located styles of the agent runs table: heading row, bordered table, native checkbox. */
export const s = {
  section: { display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  title: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    margin: 0,
    fontSize: 12,
    fontWeight: 600,
    letterSpacing: "0.08em",
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  caption: { flex: 1, fontSize: 12.5, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  scroll: { overflowX: "auto", border: "1px solid var(--border)", borderRadius: 9 } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: {
    position: "relative",
    textAlign: "left",
    padding: "9px 12px",
    fontSize: 11.5,
    fontWeight: 600,
    letterSpacing: "0.06em",
    color: "var(--text-muted)",
    background: "var(--bg-surface)",
    borderBottom: "1px solid var(--border)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  num: { textAlign: "right" } satisfies CSSProperties,
  td: { padding: "9px 12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap" } satisfies CSSProperties,
  version: { color: "var(--accent-text)", fontWeight: 600 } satisfies CSSProperties,
  selected: { background: "var(--bg-hover)" } satisfies CSSProperties,
  checkbox: { width: 16, height: 16, accentColor: "var(--accent)", cursor: "pointer" } satisfies CSSProperties,
  srOnly: { position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)", whiteSpace: "nowrap" } satisfies CSSProperties,
  empty: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
} as const;
