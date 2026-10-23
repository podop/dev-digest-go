import type { CSSProperties } from "react";

export const s = {
  chips: { display: "inline-flex", gap: 6, fontSize: 11.5, fontWeight: 600 } satisfies CSSProperties,
  /** `color` null ⇒ muted (no findings of that severity). */
  chip: (color: string | null): CSSProperties => ({
    color: color ?? "var(--text-muted)",
    opacity: color ? 1 : 0.7,
  }),
} as const;
