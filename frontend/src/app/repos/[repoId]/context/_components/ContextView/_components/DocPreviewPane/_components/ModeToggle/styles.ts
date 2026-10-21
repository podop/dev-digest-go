import type { CSSProperties } from "react";

/** Co-located styles for ModeToggle. */
export const s = {
  group: {
    display: "inline-flex",
    padding: 2,
    gap: 2,
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  btn: {
    padding: "5px 12px",
    border: "none",
    borderRadius: 6,
    background: "transparent",
    color: "var(--text-muted)",
    fontSize: 13,
    fontWeight: 500,
    cursor: "pointer",
  } satisfies CSSProperties,
  btnOn: { background: "var(--bg-hover)", color: "var(--text-primary)", fontWeight: 600 } satisfies CSSProperties,
  btnOff: { cursor: "not-allowed", opacity: 0.5 } satisfies CSSProperties,
} as const;
