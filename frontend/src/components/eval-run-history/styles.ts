import type { CSSProperties } from "react";

/** Co-located styles for the eval runs table. */
export const s = {
  scroll: { overflowX: "auto", border: "1px solid var(--border)", borderRadius: 9 } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: {
    textAlign: "left",
    padding: "9px 12px",
    fontSize: 12,
    fontWeight: 600,
    color: "var(--text-muted)",
    background: "var(--bg-surface)",
    borderBottom: "1px solid var(--border)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  td: { padding: "9px 12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap" } satisfies CSSProperties,
  num: { textAlign: "right" } satisfies CSSProperties,
  selected: { background: "var(--bg-hover)" } satisfies CSSProperties,
  checkbox: { width: 16, height: 16, accentColor: "var(--accent)", cursor: "pointer" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  crit: { color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  srOnly: { position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)", whiteSpace: "nowrap" } satisfies CSSProperties,
  agent: { fontWeight: 600 } satisfies CSSProperties,
} as const;
