import type { CSSProperties } from "react";

/** Co-located styles for RepoRow. */
export const s = {
  row: {
    display: "flex",
    alignItems: "center",
    gap: 14,
    padding: "11px 16px",
    borderTop: "1px solid var(--border)",
  } satisfies CSSProperties,
  repo: {
    flex: 1,
    minWidth: 0,
    fontSize: 13.5,
    fontWeight: 600,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  target: { flexShrink: 0, display: "inline-flex", alignItems: "center", gap: 8 } satisfies CSSProperties,
  host: { fontSize: 12, color: "var(--text-secondary)", maxWidth: 160, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  run: {
    flexShrink: 0,
    maxWidth: 220,
    fontSize: 12.5,
    color: "var(--text-secondary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
  runLink: { color: "var(--accent)", textDecoration: "none" } satisfies CSSProperties,
  manageLink: {
    display: "inline-flex",
    alignItems: "center",
    padding: "5px 9px",
    fontSize: 12.5,
    fontWeight: 500,
    borderRadius: 6,
    border: "1px solid var(--border-strong)",
    background: "var(--bg-elevated)",
    color: "var(--text-primary)",
    textDecoration: "none",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
