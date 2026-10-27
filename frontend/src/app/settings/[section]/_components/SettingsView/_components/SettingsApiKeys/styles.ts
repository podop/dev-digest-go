import type { CSSProperties } from "react";

/** Co-located styles for SettingsApiKeys (the info note's path and links; the box itself is the kit InfoNote). */
export const s = {
  path: { fontFamily: "var(--font-mono)", color: "var(--text-secondary)", wordBreak: "break-all" } satisfies CSSProperties,
  link: { color: "var(--text-secondary)", fontWeight: 700 } satisfies CSSProperties,
} as const;
