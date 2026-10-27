import type { CSSProperties } from "react";

/** Co-located styles for a setup step's title, body and action row (prototype ScreenOnboarding). */
export const s = {
  title: { fontSize: 21, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  sub: {
    fontSize: 13.5,
    color: "var(--text-secondary)",
    margin: "6px 0 24px",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  /** The last block of a step (field, callout) keeps its 18px bottom margin: 18 + 6 = the prototype's 24. */
  actions: { display: "flex", gap: 10, alignItems: "center", marginTop: 6 } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
} as const;
