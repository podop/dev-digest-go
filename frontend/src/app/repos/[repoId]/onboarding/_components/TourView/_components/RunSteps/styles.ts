import type { CSSProperties } from "react";

/** Co-located styles for RunSteps. */
export const s = {
  empty: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  warning: { margin: "0 0 10px", fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  row: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "10px 12px",
    background: "var(--bg-primary)",
    border: "1px solid var(--border)",
    borderRadius: 8,
    minWidth: 0,
  } satisfies CSSProperties,
  num: { flexShrink: 0, width: 16, fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
  text: { flex: 1, minWidth: 0, display: "flex", flexWrap: "wrap", alignItems: "baseline", columnGap: 10 } satisfies CSSProperties,
  /** Long commands wrap instead of widening the page. */
  command: {
    fontSize: 13,
    fontWeight: 600,
    color: "var(--text-primary)",
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere",
    minWidth: 0,
  } satisfies CSSProperties,
  comment: { fontSize: 12.5, color: "var(--text-muted)", overflowWrap: "anywhere", minWidth: 0 } satisfies CSSProperties,
  copyBtn: {
    flexShrink: 0,
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    padding: 6,
    borderRadius: 6,
    fontSize: 12,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  copiedText: { flexShrink: 0, fontSize: 12, color: "var(--ok)" } satisfies CSSProperties,
  copyDone: { color: "var(--ok)" } satisfies CSSProperties,
} as const;
