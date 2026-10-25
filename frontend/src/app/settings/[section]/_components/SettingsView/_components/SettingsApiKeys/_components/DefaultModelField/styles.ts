import type { CSSProperties } from "react";

/** Co-located styles for DefaultModelField. */
export const s = {
  // A <fieldset> (named by aria-label) groups the pickers; reset its browser chrome.
  row: { display: "flex", gap: 10, alignItems: "center", border: 0, margin: 0, padding: 0, minWidth: 0 } satisfies CSSProperties,
  provider: { width: 170, flexShrink: 0 } satisfies CSSProperties,
  model: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  note: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    marginTop: 10,
    fontSize: 12.5,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  warn: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    marginTop: 10,
    fontSize: 13,
    fontWeight: 600,
    color: "var(--warn)",
  } satisfies CSSProperties,
  error: { marginTop: 10, fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
} as const;
