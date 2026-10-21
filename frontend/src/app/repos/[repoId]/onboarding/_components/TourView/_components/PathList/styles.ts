import type { CSSProperties } from "react";

/** Co-located styles for PathList. */
export const s = {
  empty: { margin: 0, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  criticalList: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  criticalRow: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "10px 12px",
    background: "var(--bg-primary)",
    border: "1px solid var(--border)",
    borderRadius: 8,
    minWidth: 0,
  } satisfies CSSProperties,
  icon: { flexShrink: 0, color: "var(--text-muted)" } satisfies CSSProperties,
  path: {
    fontSize: 12.5,
    color: "var(--text-primary)",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    minWidth: 0,
  } satisfies CSSProperties,
  pathInline: { flex: "0 1 auto", maxWidth: "55%" } satisfies CSSProperties,
  reasonInline: { flex: "1 1 0", minWidth: 0, fontSize: 13, color: "var(--text-secondary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  readingList: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 12 } satisfies CSSProperties,
  readingRow: { display: "flex", alignItems: "flex-start", gap: 12, minWidth: 0 } satisfies CSSProperties,
  num: {
    flexShrink: 0,
    width: 22,
    height: 22,
    borderRadius: 99,
    display: "grid",
    placeItems: "center",
    fontSize: 11,
    fontWeight: 700,
    background: "var(--accent-bg)",
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  readingText: { display: "flex", flexDirection: "column", gap: 2, minWidth: 0, flex: 1 } satisfies CSSProperties,
  reasonBelow: { fontSize: 13, color: "var(--text-muted)", overflowWrap: "anywhere" } satisfies CSSProperties,
} as const;
