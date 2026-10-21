import type { CSSProperties } from "react";

/** Co-located styles of the run-table cells (/eval agent runs table and overview recent runs). */
export const s = {
  cell: { padding: "9px 12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap" } satisfies CSSProperties,
  metric: { display: "flex", alignItems: "center", gap: 8 } satisfies CSSProperties,
  track: { flex: 1, minWidth: 64, height: 6, borderRadius: 3, background: "var(--bg-hover)", overflow: "hidden" } satisfies CSSProperties,
  fill: (percent: number, color: string): CSSProperties => ({ width: `${percent}%`, height: "100%", borderRadius: 3, background: color }),
  percent: { minWidth: 36, textAlign: "right", fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  pass: { fontWeight: 700, color: "var(--text-primary)" } satisfies CSSProperties,
  progress: { color: "var(--text-secondary)" } satisfies CSSProperties,
  crit: { color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
} as const;
