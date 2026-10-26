import type { CSSProperties } from "react";

/** Co-located styles for step 2 (host choice, "Already connected"). */
export const s = {
  group: { border: "none", padding: 0, margin: "0 0 20px" } satisfies CSSProperties,
  legend: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)", padding: 0, marginBottom: 8 } satisfies CSSProperties,
  options: { display: "flex", gap: 8 } satisfies CSSProperties,
  option: (on: boolean): CSSProperties => ({
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    padding: "8px 14px",
    borderRadius: 8,
    fontSize: 14,
    cursor: "pointer",
    color: "var(--text-primary)",
    background: on ? "var(--bg-hover)" : "transparent",
    border: `1px solid ${on ? "var(--accent)" : "var(--border-strong)"}`,
  }),
  connected: {
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    marginTop: 4,
    fontSize: 13,
    color: "var(--ok)",
  } satisfies CSSProperties,
} as const;
