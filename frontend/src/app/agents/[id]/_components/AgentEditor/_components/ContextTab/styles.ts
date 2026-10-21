import type { CSSProperties } from "react";

/** Co-located styles for the agent ContextTab. */
export const s = {
  wrap: { maxWidth: 820 } satisfies CSSProperties,
  footer: {
    display: "flex",
    alignItems: "baseline",
    gap: 16,
    flexWrap: "wrap",
    marginTop: 16,
    paddingTop: 14,
    borderTop: "1px solid var(--border)",
    fontSize: 13,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  total: { color: "var(--text-primary)", fontWeight: 600 } satisfies CSSProperties,
  note: { marginLeft: "auto" } satisfies CSSProperties,
  warning: {
    flexBasis: "100%",
    display: "flex",
    alignItems: "center",
    gap: 8,
    color: "var(--warn)",
  } satisfies CSSProperties,
} as const;
