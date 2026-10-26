import type { CSSProperties } from "react";

/** Co-located styles for AgentRow. */
export const s = {
  row: { padding: "10px 14px", borderTop: "1px solid var(--border)" } satisfies CSSProperties,
  line: { display: "flex", alignItems: "center", gap: 10, minWidth: 0 } satisfies CSSProperties,
  label: (disabled: boolean): CSSProperties => ({
    display: "flex",
    alignItems: "center",
    gap: 10,
    flex: 1,
    minWidth: 0,
    fontSize: 13.5,
    color: disabled ? "var(--text-muted)" : "var(--text-primary)",
    cursor: disabled ? "not-allowed" : "pointer",
  }),
  name: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  reason: { fontSize: 12, color: "var(--text-muted)", marginTop: 4, marginLeft: 26, lineHeight: 1.4 } satisfies CSSProperties,
  progress: { fontSize: 12.5, marginTop: 6, marginLeft: 26, display: "flex", alignItems: "center", gap: 6 } satisfies CSSProperties,
  spinner: { animation: "ddspin 1s linear infinite", flexShrink: 0 } satisfies CSSProperties,
  ok: { color: "var(--ok)", fontWeight: 600 } satisfies CSSProperties,
  fail: { color: "var(--crit)", fontWeight: 600 } satisfies CSSProperties,
  prLink: { color: "var(--accent)", fontWeight: 500 } satisfies CSSProperties,
} as const;
