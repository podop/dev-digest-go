import type { CSSProperties } from "react";

/** Numbers from Claude Design screen_multiagent.jsx (AgentFindingMini). */
export const s = {
  row: (color: string): CSSProperties => ({
    padding: "8px 10px",
    borderRadius: 6,
    background: "var(--bg-surface)",
    borderLeft: `2px solid ${color}`,
  }),
  titleRow: { display: "flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  icon: (color: string): CSSProperties => ({ display: "inline-flex", color, flexShrink: 0 }),
  title: { fontSize: 12, fontWeight: 600, lineHeight: 1.3, color: "var(--text-primary)", overflowWrap: "anywhere" } satisfies CSSProperties,
  location: { fontSize: 10.5, color: "var(--text-muted)", marginTop: 4, overflowWrap: "anywhere" } satisfies CSSProperties,
} as const;
