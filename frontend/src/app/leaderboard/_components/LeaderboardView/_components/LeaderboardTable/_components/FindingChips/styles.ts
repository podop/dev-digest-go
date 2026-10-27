import type { CSSProperties } from "react";

export const s = {
  chips: { display: "inline-flex", gap: 5, fontSize: 10.5, fontWeight: 600 } satisfies CSSProperties,
  /** `color` null ⇒ muted (no findings of that severity); `outlined` draws the red ring of a CRITICAL chip with open findings. */
  chip: (color: string | null, outlined = false): CSSProperties => ({
    color: color ?? "var(--text-muted)",
    opacity: color ? 1 : 0.7,
    ...(outlined ? { borderWidth: 1, borderStyle: "solid", borderColor: "var(--crit)", borderRadius: 4, padding: "0 4px" } : null),
  }),
} as const;
