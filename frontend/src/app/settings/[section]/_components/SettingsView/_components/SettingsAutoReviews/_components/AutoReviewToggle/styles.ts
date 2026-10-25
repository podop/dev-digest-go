import type { CSSProperties } from "react";

/** Co-located styles for AutoReviewToggle. */
export const s = {
  // A <fieldset> only to disable the vendored Toggle: reset its default border, padding and min-width.
  card: {
    margin: "0 0 20px",
    minWidth: 0,
    padding: "14px 16px",
    borderRadius: 8,
    border: "1px solid var(--border)",
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  row: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 16,
    cursor: "pointer",
  } satisfies CSSProperties,
  title: { fontSize: 14, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  subline: { marginTop: 4, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
