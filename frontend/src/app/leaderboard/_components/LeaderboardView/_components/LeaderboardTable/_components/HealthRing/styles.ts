import type { CSSProperties } from "react";

export const s = {
  ring: (size: number): CSSProperties => ({ position: "relative", width: size, height: size, flexShrink: 0 }),
  svg: { transform: "rotate(-90deg)" } satisfies CSSProperties,
  value: { position: "absolute", inset: 0, display: "grid", placeItems: "center", fontSize: 11, fontWeight: 700 } satisfies CSSProperties,
} as const;
