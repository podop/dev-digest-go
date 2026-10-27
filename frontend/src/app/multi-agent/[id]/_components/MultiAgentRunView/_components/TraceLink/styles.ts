import type { CSSProperties } from "react";

export const s = {
  link: (hover: boolean): CSSProperties => ({
    background: "none",
    border: "none",
    padding: 0,
    fontSize: 12,
    cursor: "pointer",
    color: hover ? "var(--accent-text)" : "var(--text-secondary)",
    textDecoration: hover ? "underline" : "none",
    textUnderlineOffset: 2,
  }),
} as const;
