import type { CSSProperties } from "react";

/** Co-located styles of the overview's recent runs table. The header row collapses to zero
 *  height; its text stays in the accessibility tree (srOnly sits in a positioned cell). */
export const s = {
  scroll: { overflowX: "auto", border: "1px solid var(--border)", borderRadius: 9, background: "var(--bg-surface)" } satisfies CSSProperties,
  table: { width: "100%", borderCollapse: "collapse", fontSize: 13 } satisfies CSSProperties,
  th: { position: "relative", padding: 0, border: 0, height: 0, lineHeight: 0 } satisfies CSSProperties,
  srOnly: { position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)", whiteSpace: "nowrap" } satisfies CSSProperties,
  td: { padding: "11px 12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap" } satisfies CSSProperties,
  agent: { fontWeight: 700, color: "var(--text-primary)" } satisfies CSSProperties,
  stamp: { fontSize: 12.5, color: "var(--text-secondary)" } satisfies CSSProperties,
  version: { color: "var(--accent-text)", fontWeight: 600 } satisfies CSSProperties,
} as const;
