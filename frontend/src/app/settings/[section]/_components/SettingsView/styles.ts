import type { CSSProperties } from "react";

/** Co-located styles for the SettingsView shell (left sub-nav + section pane), sized as the prototype's ScreenSettings. */
export const s = {
  layout: { display: "flex", minHeight: "calc(100 * var(--app-vh) - 52px)" } satisfies CSSProperties,
  nav: {
    width: 210,
    flexShrink: 0,
    borderRight: "1px solid var(--border)",
    padding: 14,
    background: "var(--bg-surface)",
  } satisfies CSSProperties,
  navTitle: { fontSize: 15, fontWeight: 700, padding: "2px 8px 12px" } satisfies CSSProperties,
  navItem: (on: boolean): CSSProperties => ({
    padding: "7px 10px",
    borderRadius: 6,
    fontSize: 13,
    fontWeight: on ? 600 : 500,
    cursor: "pointer",
    color: on ? "var(--text-primary)" : "var(--text-secondary)",
    background: on ? "var(--bg-hover)" : "transparent",
    marginBottom: 2,
  }),
  navIcon: { marginRight: 7, verticalAlign: "-2px" } satisfies CSSProperties,
  pane: { flex: 1, minWidth: 0, overflow: "auto", padding: "24px 28px" } satisfies CSSProperties,
} as const;
