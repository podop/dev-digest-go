import type { CSSProperties } from "react";

/** Co-located styles for ContextView. */
export const s = {
  page: { display: "flex", minHeight: "100%", alignItems: "stretch" } satisfies CSSProperties,
  side: {
    width: 300,
    flexShrink: 0,
    borderRight: "1px solid var(--border)",
    padding: "16px 12px",
    display: "flex",
    flexDirection: "column",
    gap: 12,
  } satisfies CSSProperties,
  sideHead: { display: "flex", alignItems: "flex-start", gap: 8, padding: "0 4px" } satisfies CSSProperties,
  sideHeadText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  label: { fontSize: 11, fontWeight: 600, letterSpacing: "0.06em", textTransform: "uppercase", color: "var(--text-muted)" } satisfies CSSProperties,
  globs: { fontSize: 12, color: "var(--text-secondary)", marginTop: 4, wordBreak: "break-all" } satisfies CSSProperties,
  summary: { fontSize: 12, color: "var(--text-muted)", marginTop: 4 } satisfies CSSProperties,
  muted: { fontSize: 12, color: "var(--text-muted)", padding: "0 4px" } satisfies CSSProperties,
  rows: { display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
} as const;
