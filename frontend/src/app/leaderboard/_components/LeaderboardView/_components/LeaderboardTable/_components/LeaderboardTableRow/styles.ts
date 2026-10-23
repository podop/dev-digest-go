import type { CSSProperties } from "react";

export const s = {
  td: { padding: "12px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap", fontSize: 13, verticalAlign: "middle" } satisfies CSSProperties,
  row: { cursor: "pointer" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  rank: { color: "var(--text-muted)", fontWeight: 600 } satisfies CSSProperties,
  prCell: { maxWidth: 340 } satisfies CSSProperties,
  prMeta: { display: "flex", alignItems: "center", gap: 6, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  repoNumber: { color: "var(--text-secondary)", fontWeight: 600, textDecoration: "none" } satisfies CSSProperties,
  title: { fontWeight: 700, fontSize: 13.5, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", marginTop: 3 } satisfies CSSProperties,
  stack: { display: "flex", flexDirection: "column", alignItems: "flex-start", gap: 4 } satisfies CSSProperties,
  sizeBadge: (color: string): CSSProperties => ({ border: `1px solid ${color}` }),
  sizeStats: { fontSize: 11.5 } satisfies CSSProperties,
  add: { color: "var(--ok)" } satisfies CSSProperties,
  del: { color: "var(--crit)" } satisfies CSSProperties,
  risk: { display: "inline-flex", alignItems: "center", gap: 6, fontSize: 12.5 } satisfies CSSProperties,
  riskDot: (color: string): CSSProperties => ({ width: 7, height: 7, borderRadius: 99, background: color, flex: "none" }),
  time: { display: "flex", flexDirection: "column", gap: 2, fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
  detail: { padding: "14px 16px 16px 58px", borderBottom: "1px solid var(--border)", background: "var(--bg-surface)" } satisfies CSSProperties,
  toggle: { all: "unset", cursor: "pointer", color: "var(--text-muted)", width: 16, textAlign: "center" } satisfies CSSProperties,
} as const;
