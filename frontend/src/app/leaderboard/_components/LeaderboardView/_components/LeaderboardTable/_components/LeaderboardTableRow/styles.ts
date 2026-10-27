import type { CSSProperties } from "react";

export const s = {
  td: { padding: "4px 8px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap", fontSize: 11.5, verticalAlign: "middle" } satisfies CSSProperties,
  row: { cursor: "pointer" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  rank: { color: "var(--text-muted)", fontWeight: 600 } satisfies CSSProperties,
  prCell: { maxWidth: 300 } satisfies CSSProperties,
  prMeta: { display: "flex", alignItems: "center", gap: 5, fontSize: 10.5, color: "var(--text-muted)" } satisfies CSSProperties,
  repoNumber: { color: "var(--text-secondary)", fontWeight: 600, textDecoration: "none" } satisfies CSSProperties,
  title: { fontWeight: 650, fontSize: 12, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", marginTop: 1 } satisfies CSSProperties,
  /** Size badge and line stats on one line, so a row stays two text lines tall. */
  stack: { display: "flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  sizeBadge: (color: string): CSSProperties => ({ border: `1px solid ${color}`, fontSize: 10, padding: "0 5px", lineHeight: 1.4 }),
  statusBadge: { fontSize: 10.5, padding: 0 } satisfies CSSProperties,
  sizeStats: { fontSize: 10 } satisfies CSSProperties,
  add: { color: "var(--ok)" } satisfies CSSProperties,
  del: { color: "var(--crit)" } satisfies CSSProperties,
  risk: { display: "inline-flex", alignItems: "center", gap: 5, fontSize: 11 } satisfies CSSProperties,
  riskDot: (color: string): CSSProperties => ({ width: 7, height: 7, borderRadius: 99, background: color, flex: "none" }),
  time: { display: "flex", flexDirection: "column", gap: 0, fontSize: 10.5, color: "var(--text-secondary)" } satisfies CSSProperties,
  detail: { padding: "10px 12px 12px 48px", borderBottom: "1px solid var(--border)", background: "var(--bg-surface)" } satisfies CSSProperties,
  toggle: { all: "unset", cursor: "pointer", color: "var(--text-muted)", width: 16, textAlign: "center" } satisfies CSSProperties,
} as const;
