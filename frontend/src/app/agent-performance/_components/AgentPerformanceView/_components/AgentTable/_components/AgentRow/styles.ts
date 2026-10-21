import type { CSSProperties } from "react";

export const s = {
  td: { padding: "12px 14px", borderBottom: "1px solid var(--border)", whiteSpace: "nowrap", fontSize: 13 } satisfies CSSProperties,
  row: { cursor: "pointer" } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  toggle: {
    all: "unset",
    cursor: "pointer",
    display: "inline-flex",
    alignItems: "center",
    gap: 10,
    maxWidth: 300,
    fontSize: 13.5,
    fontWeight: 600,
  } satisfies CSSProperties,
  name: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  chevron: { color: "var(--text-muted)", width: 10, flex: "none" } satisfies CSSProperties,
  link: { color: "var(--accent-text)" } satisfies CSSProperties,
  detail: { padding: "14px 16px 16px 58px", borderBottom: "1px solid var(--border)", background: "var(--bg-surface)" } satisfies CSSProperties,
  trend: { display: "flex", alignItems: "center", gap: 16, marginBottom: 14 } satisfies CSSProperties,
  trendCaption: { fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
