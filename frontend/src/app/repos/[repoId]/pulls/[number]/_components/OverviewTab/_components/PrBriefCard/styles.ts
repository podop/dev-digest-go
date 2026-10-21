import type { CSSProperties } from "react";

export const s = {
  card: {
    border: "1px solid var(--border)",
    borderRadius: 8,
    background: "var(--bg-elevated)",
    padding: 18,
    display: "flex",
    flexDirection: "column",
    gap: 16,
    minWidth: 0,
  } satisfies CSSProperties,
  body: {
    display: "flex",
    flexDirection: "column",
    gap: 10,
    minWidth: 0,
  } satisfies CSSProperties,
  emptyTitle: {
    margin: 0,
    fontSize: 14,
    fontWeight: 600,
    color: "var(--text)",
  } satisfies CSSProperties,
  explanation: {
    margin: 0,
    fontSize: 13.5,
    lineHeight: 1.55,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  notes: {
    display: "flex",
    flexDirection: "column",
    gap: 4,
  } satisfies CSSProperties,
  note: {
    margin: 0,
    fontSize: 12,
    color: "var(--text-muted)",
    fontStyle: "italic",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
  staleNote: {
    margin: 0,
    fontSize: 12.5,
    fontWeight: 600,
    color: "var(--warn)",
  } satisfies CSSProperties,
  errorNote: {
    margin: 0,
    fontSize: 12.5,
    color: "var(--crit)",
    overflowWrap: "anywhere",
  } satisfies CSSProperties,
} as const;
