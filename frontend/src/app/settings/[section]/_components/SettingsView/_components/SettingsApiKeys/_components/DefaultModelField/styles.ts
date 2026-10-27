import type { CSSProperties } from "react";

/** Co-located styles for DefaultModelField (prototype FormField sizes: notes 11.5–12px under the control). */
export const s = {
  // A <fieldset> (named by aria-label) groups the pickers; reset its browser chrome.
  row: { display: "flex", gap: 8, alignItems: "center", border: 0, margin: 0, padding: 0, minWidth: 0 } satisfies CSSProperties,
  provider: { width: 170, flexShrink: 0 } satisfies CSSProperties,
  model: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  note: {
    display: "flex",
    alignItems: "flex-start",
    gap: 6,
    marginTop: 7,
    fontSize: 11.5,
    lineHeight: 1.45,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  noteIcon: { flexShrink: 0, marginTop: 1 } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "flex-start",
    gap: 6,
    marginTop: 7,
    fontSize: 12,
    fontWeight: 600,
    lineHeight: 1.45,
    color: "var(--warn)",
  } satisfies CSSProperties,
  error: { marginTop: 7, fontSize: 12, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
} as const;
