import type { CSSProperties } from "react";

/** Co-located styles for SelectionList. */
export const s = {
  group: {
    position: "relative",
    display: "flex",
    flexDirection: "column",
    gap: 6,
    border: 0,
    margin: 0,
    padding: 0,
    minWidth: 0,
  } satisfies CSSProperties,
  toggle: { position: "absolute", top: -6, right: 0 } satisfies CSSProperties,
  heading: {
    fontSize: 12,
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.04em",
    color: "var(--text-muted)",
    padding: 0,
    marginBottom: 6,
  } satisfies CSSProperties,
  list: {
    listStyle: "none",
    margin: 0,
    padding: "8px 10px",
    maxHeight: 168,
    overflowY: "auto",
    border: "1px solid var(--border)",
    borderRadius: 7,
    display: "flex",
    flexDirection: "column",
    gap: 8,
  } satisfies CSSProperties,
  item: { minWidth: 0 } satisfies CSSProperties,
  itemName: {
    display: "block",
    maxWidth: 420,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  empty: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
