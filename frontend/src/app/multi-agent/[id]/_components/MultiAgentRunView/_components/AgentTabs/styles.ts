import type { CSSProperties } from "react";

export const s = {
  tabs: { overflowX: "auto" } satisfies CSSProperties,
  panel: { display: "flex", flexDirection: "column", gap: 14, paddingTop: 16 } satisfies CSSProperties,
  summary: {
    display: "flex",
    alignItems: "flex-start",
    gap: 16,
    padding: 16,
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  summaryMain: { flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  summaryHead: { display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  name: { fontSize: 15, fontWeight: 600, margin: 0 } satisfies CSSProperties,
  meta: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  prose: { fontSize: 14, color: "var(--text-secondary)" } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  error: { margin: 0, fontSize: 13, color: "var(--crit)", overflowWrap: "anywhere" } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
} as const;
