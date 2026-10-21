import type { CSSProperties } from "react";

/** Co-located styles for the skill ContextTab. */
export const s = {
  wrap: { maxWidth: 820 } satisfies CSSProperties,
  label: {
    margin: "20px 0 8px",
    fontSize: 11,
    fontWeight: 600,
    letterSpacing: "0.06em",
    textTransform: "uppercase",
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  block: {
    margin: 0,
    padding: "12px 16px",
    borderRadius: 8,
    border: "1px solid var(--border)",
    background: "var(--bg-surface)",
    fontSize: 13,
    color: "var(--text-secondary)",
    whiteSpace: "pre-wrap",
    wordBreak: "break-all",
  } satisfies CSSProperties,
} as const;
