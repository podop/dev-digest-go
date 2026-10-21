import type { CSSProperties } from "react";

export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 10, overflowX: "auto" } satisfies CSSProperties,
  svg: { display: "block", maxWidth: "100%", height: "auto" } satisfies CSSProperties,
  legend: {
    listStyle: "none",
    margin: 0,
    padding: 0,
    display: "flex",
    gap: 16,
    flexWrap: "wrap",
    fontSize: 12,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  legendItem: { display: "inline-flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  swatch: (fill: string, stroke: string): CSSProperties => ({
    width: 12,
    height: 12,
    borderRadius: 3,
    background: fill,
    border: `1px solid ${stroke}`,
  }),
  muted: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;

/** Node colors by kind — theme tokens, so the drawing follows light/dark. */
export const NODE_COLORS = {
  symbol: { fill: "var(--accent-bg)", stroke: "var(--accent)" },
  caller: { fill: "var(--bg-hover)", stroke: "var(--border-strong)" },
  endpoint: { fill: "var(--bg-hover)", stroke: "var(--text-secondary)" },
  cron: { fill: "var(--warn-bg)", stroke: "var(--warn)" },
} as const;
