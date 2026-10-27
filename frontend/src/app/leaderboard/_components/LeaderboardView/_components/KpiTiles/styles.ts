import type { CSSProperties } from "react";

export const s = {
  /** All eight tiles in one row (user, 2026-10-27). */
  grid: { display: "flex", gap: 8, minWidth: 0 } satisfies CSSProperties,
  alert: { color: "var(--crit)" } satisfies CSSProperties,
  delta: { position: "relative", fontSize: 10, fontWeight: 600, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", minWidth: 0 } satisfies CSSProperties,
  srOnly: {
    position: "absolute",
    width: 1,
    height: 1,
    overflow: "hidden",
    clip: "rect(0 0 0 0)",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
