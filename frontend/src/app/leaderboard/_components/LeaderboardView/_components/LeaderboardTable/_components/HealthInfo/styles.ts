import type { CSSProperties } from "react";

export const s = {
  wrap: { display: "inline-flex", marginLeft: 4, verticalAlign: "middle" } satisfies CSSProperties,
  button: { all: "unset", cursor: "help", display: "inline-flex", color: "var(--text-muted)" } satisfies CSSProperties,
  tooltip: {
    position: "fixed",
    zIndex: 70,
    width: 280,
    padding: 12,
    borderRadius: 8,
    background: "var(--bg-surface)",
    border: "1px solid var(--border-strong)",
    boxShadow: "0 8px 24px rgba(0,0,0,.35)",
    textTransform: "none",
    letterSpacing: 0,
    fontWeight: 400,
    whiteSpace: "normal",
    color: "var(--text-secondary)",
    fontSize: 12,
    lineHeight: 1.5,
  } satisfies CSSProperties,
  formula: { fontSize: 11, color: "var(--text-primary)", marginBottom: 6 } satisfies CSSProperties,
} as const;
