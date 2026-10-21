import type { CSSProperties } from "react";

export const s = {
  row: { display: "flex", gap: 14, flexWrap: "wrap" } satisfies CSSProperties,
  marker: { color: "var(--warn)", cursor: "help" } satisfies CSSProperties,
  agent: { display: "flex", alignItems: "center", gap: 10, marginTop: 4, minWidth: 0 } satisfies CSSProperties,
  agentText: { display: "flex", flexDirection: "column", minWidth: 0, letterSpacing: 0 } satisfies CSSProperties,
  name: { display: "block", maxWidth: 200, fontSize: 14.5, fontWeight: 700, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } satisfies CSSProperties,
  agentSub: { fontSize: 11.5, fontWeight: 400, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
