import type { CSSProperties } from "react";

export const s = {
  doneLink: {
    display: "inline-flex",
    alignItems: "center",
    gap: 5,
    fontSize: 12.5,
    color: "var(--accent-text)",
    textDecoration: "none",
  } satisfies CSSProperties,
  /** Full-width line under the actions row (the row wraps). */
  error: {
    flexBasis: "100%",
    fontSize: 12.5,
    color: "var(--crit)",
  } satisfies CSSProperties,
} as const;
