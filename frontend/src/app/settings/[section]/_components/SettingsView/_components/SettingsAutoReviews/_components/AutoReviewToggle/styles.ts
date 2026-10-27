import type { CSSProperties } from "react";

/** Co-located styles for AutoReviewToggle — the prototype's toggle box (switch left, title + subline right). */
export const s = {
  // A <fieldset> only to disable the vendored Toggle: reset its default border and min-width. A 2-column grid
  // puts the switch left of the title and the subline; the <label> is display:contents so it adds no box.
  card: {
    margin: "0 0 18px",
    minWidth: 0,
    display: "grid",
    gridTemplateColumns: "auto 1fr",
    columnGap: 12,
    alignItems: "center",
    padding: "14px 16px",
    borderRadius: 9,
    border: "1px solid var(--border)",
    background: "var(--bg-elevated)",
  } satisfies CSSProperties,
  label: { display: "contents", cursor: "pointer" } satisfies CSSProperties,
  switch: { gridRow: "1 / span 2", display: "inline-flex" } satisfies CSSProperties,
  title: { fontSize: 13.5, fontWeight: 600, color: "var(--text-primary)", cursor: "pointer" } satisfies CSSProperties,
  subline: { gridColumn: 2, fontSize: 12, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
