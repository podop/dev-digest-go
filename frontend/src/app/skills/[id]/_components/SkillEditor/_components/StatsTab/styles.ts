import type { CSSProperties } from "react";

/** Co-located styles for StatsTab. */
export const s = {
  wrap: { maxWidth: 1240, display: "flex", flexDirection: "column", gap: 20 } satisfies CSSProperties,
  cards: { display: "flex", gap: 12, flexWrap: "wrap" } satisfies CSSProperties,
  grid: { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(340px, 1fr))", gap: 16, alignItems: "start" } satisfies CSSProperties,
  muted: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  donut: { display: "flex", justifyContent: "center", paddingTop: 4 } satisfies CSSProperties,
  agents: { listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  agent: (enabled: boolean): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "8px 12px",
    border: "1px solid var(--border)",
    borderRadius: 7,
    background: "var(--bg-surface)",
    color: enabled ? "var(--text-primary)" : "var(--text-muted)",
    textDecoration: "none",
  }),
  agentName: { flex: 1, minWidth: 0, fontSize: 13.5, fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  open: { fontSize: 12, color: "var(--text-secondary)" } satisfies CSSProperties,
} as const;
