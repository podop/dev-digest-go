import type { CSSProperties } from "react";

/** Co-located styles for step 2 (login and scopes under the test result, the missing-scope warning). */
export const s = {
  who: { fontSize: 12, color: "var(--text-muted)", lineHeight: 1.5, margin: "-8px 0 18px" } satisfies CSSProperties,
  login: { fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  warn: { marginTop: 10, marginBottom: 0, alignItems: "flex-start" } satisfies CSSProperties,
} as const;
