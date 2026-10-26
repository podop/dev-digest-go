import type { CSSProperties } from "react";

/** Co-located styles for SettingsModels (per-feature model picker). */
export const s = {
  wrap: { maxWidth: 640 } satisfies CSSProperties,
  row: { marginBottom: 18 } satisfies CSSProperties,
  pickers: { display: "flex", gap: 10, alignItems: "flex-start" } satisfies CSSProperties,
  provider: { width: 170, flexShrink: 0 } satisfies CSSProperties,
  model: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  blocked: { fontSize: 13, fontWeight: 600, color: "var(--warn)" } satisfies CSSProperties,
  defaultTag: {
    marginLeft: 8,
    fontSize: 12,
    fontWeight: 500,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  note: {
    display: "flex",
    gap: 10,
    marginTop: 8,
    padding: "12px 16px",
    borderRadius: 8,
    border: "1px solid var(--border)",
    background: "var(--bg-surface)",
    fontSize: 13,
    color: "var(--text-muted)",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  noteIcon: { flexShrink: 0, marginTop: 1 } satisfies CSSProperties,
} as const;
