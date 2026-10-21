import type { CSSProperties } from "react";

export const s = {
  card: (muted: boolean) =>
    ({
      background: "var(--bg-elevated)",
      border: "1px solid var(--border)",
      borderRadius: 8,
      opacity: muted ? 0.8 : 1,
    }) satisfies CSSProperties,
  header: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    width: "100%",
    padding: "10px 14px",
    background: "none",
    border: "none",
    font: "inherit",
    color: "inherit",
    textAlign: "left",
    cursor: "pointer",
  } satisfies CSSProperties,
  title: { flex: 1, minWidth: 0, fontSize: 14, fontWeight: 500, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  tag: { fontSize: 12, fontWeight: 600, color: "var(--text-muted)" } satisfies CSSProperties,
  body: { display: "flex", flexDirection: "column", gap: 10, padding: "0 14px 14px" } satisfies CSSProperties,
  metaRow: { display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap", fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  prose: { fontSize: 14, color: "var(--text-secondary)" } satisfies CSSProperties,
  fixLabel: { fontSize: 12, fontWeight: 600, color: "var(--text-muted)", marginBottom: 4 } satisfies CSSProperties,
  actions: { display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" } satisfies CSSProperties,
} as const;
