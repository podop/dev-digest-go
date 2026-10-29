import type { CSSProperties } from "react";

export const s = {
  card: { display: "flex", flexDirection: "column", gap: 8, padding: "10px 12px" } satisfies CSSProperties,
  header: { display: "flex", alignItems: "baseline", gap: 10, flexWrap: "wrap" } satisfies CSSProperties,
  title: { margin: 0, fontSize: 12.5, fontWeight: 650, color: "var(--text-primary)" } satisfies CSSProperties,
  subtitle: { fontSize: 11, color: "var(--text-muted)" } satisfies CSSProperties,
  empty: { display: "flex", flexDirection: "column", gap: 2, fontSize: 11.5, color: "var(--text-muted)" } satisfies CSSProperties,
  emptyTitle: { color: "var(--text-secondary)", fontWeight: 600 } satisfies CSSProperties,
  list: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 4 } satisfies CSSProperties,
  /** The size card's grid with a wider label column (names and repositories). */
  row: { display: "grid", gridTemplateColumns: "minmax(0, 170px) 1fr 28px 120px", alignItems: "center", gap: 10 } satisfies CSSProperties,
  label: {
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    minWidth: 0,
    fontSize: 11,
    fontWeight: 600,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  labelText: { minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  repoLabel: { display: "flex", gap: 4, minWidth: 0, whiteSpace: "nowrap" } satisfies CSSProperties,
  /** Never shrinks (a weighted shrink still clipped ~1px of it): the host gives way first, max-width clips a name longer than the column. */
  repoName: { flexShrink: 0, maxWidth: "100%", overflow: "hidden", textOverflow: "ellipsis" } satisfies CSSProperties,
  repoHost: { flexShrink: 1000, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", fontWeight: 400, color: "var(--text-muted)" } satisfies CSSProperties,
  track: { height: 8, background: "var(--bg-hover)", borderRadius: 3, overflow: "hidden" } satisfies CSSProperties,
  fill: { height: "100%", borderRadius: 3 } satisfies CSSProperties,
  health: { fontSize: 11.5, fontWeight: 700, color: "var(--text-primary)", textAlign: "right" } satisfies CSSProperties,
  none: { fontSize: 11.5, color: "var(--text-muted)", textAlign: "right" } satisfies CSSProperties,
  summary: { fontSize: 10.5, color: "var(--text-secondary)", whiteSpace: "nowrap" } satisfies CSSProperties,
  footer: { display: "flex", justifyContent: "center", marginTop: "auto", paddingTop: 2 } satisfies CSSProperties,
  more: { all: "unset", cursor: "pointer", fontSize: 11, fontWeight: 600, color: "var(--accent-text)" } satisfies CSSProperties,
} as const;
