import type { CSSProperties } from "react";

export const s = {
  wrap: { display: "flex", alignItems: "center", gap: 8 } satisfies CSSProperties,
  label: { fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  select: { display: "grid", minWidth: 180 } satisfies CSSProperties,
} as const;
