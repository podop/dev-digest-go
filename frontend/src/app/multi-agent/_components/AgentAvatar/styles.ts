import type { CSSProperties } from "react";

export const s = {
  avatar: (color: string, size: number): CSSProperties => ({
    width: size,
    height: size,
    borderRadius: 8,
    display: "grid",
    placeItems: "center",
    background: `${color}1f`,
    color,
    flexShrink: 0,
  }),
} as const;
