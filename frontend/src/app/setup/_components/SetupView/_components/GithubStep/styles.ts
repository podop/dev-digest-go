import type { CSSProperties } from "react";

/** Co-located styles for step 2 (login, scopes, warning). */
export const s = {
  who: { fontSize: 13, color: "var(--text-secondary)", lineHeight: 1.6 } satisfies CSSProperties,
  login: { fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "flex-start",
    gap: 8,
    marginTop: 10,
    fontSize: 13,
    lineHeight: 1.45,
    color: "var(--text-secondary)",
  } satisfies CSSProperties,
  warnIcon: { color: "var(--warn)", flexShrink: 0, marginTop: 2 } satisfies CSSProperties,
  connected: {
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    fontSize: 13,
    color: "var(--ok)",
  } satisfies CSSProperties,
} as const;
