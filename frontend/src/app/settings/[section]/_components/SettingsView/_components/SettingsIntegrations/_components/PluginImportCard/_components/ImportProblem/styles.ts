import type { CSSProperties } from "react";

/** Co-located styles for ImportProblem. */
export const s = {
  box: { marginTop: 12, fontSize: 13, color: "var(--crit)", lineHeight: 1.5 } satisfies CSSProperties,
  message: { fontWeight: 600, overflowWrap: "anywhere" } satisfies CSSProperties,
  issues: { margin: "6px 0 0", paddingLeft: 18, fontWeight: 400, overflowWrap: "anywhere" } satisfies CSSProperties,
} as const;
