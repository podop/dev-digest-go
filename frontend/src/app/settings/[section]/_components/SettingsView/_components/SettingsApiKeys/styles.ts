import type { CSSProperties } from "react";

/** Co-located styles for SettingsApiKeys. */
export const s = {
  wrap: { maxWidth: 640 } satisfies CSSProperties,
  infoBox: {
    display: "flex",
    alignItems: "flex-start",
    gap: 10,
    marginTop: 8,
    padding: "12px 14px",
    fontSize: 13,
    lineHeight: 1.5,
    color: "var(--text-secondary)",
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  infoIcon: { flexShrink: 0, marginTop: 2, color: "var(--text-muted)" } satisfies CSSProperties,
  path: { fontFamily: "var(--font-mono)", wordBreak: "break-all" } satisfies CSSProperties,
  link: { color: "var(--accent-text)" } satisfies CSSProperties,
} as const;
