import type { CSSProperties } from "react";

/** Co-located styles for the indexing status box (prototype callout: ok when indexed, neutral while waiting). */
export const s = {
  box: (indexed: boolean): CSSProperties => ({
    display: "flex",
    flexDirection: "column",
    gap: 4,
    padding: "10px 12px",
    borderRadius: 8,
    marginBottom: 18,
    background: indexed ? "var(--ok-bg)" : "var(--bg-surface)",
    border: `1px solid ${indexed ? "rgba(16,185,129,0.25)" : "var(--border)"}`,
    fontSize: 12.5,
    color: "var(--text-secondary)",
    lineHeight: 1.45,
  }),
  line: { display: "flex", alignItems: "center", gap: 10, color: "var(--text-primary)" } satisfies CSSProperties,
  icon: (indexed: boolean): CSSProperties => ({ color: indexed ? "var(--ok)" : "var(--text-muted)", flexShrink: 0 }),
  detail: { paddingLeft: 25, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
