import type { CSSProperties } from "react";

/** Co-located styles for AgentChips. */
export const s = {
  // A <fieldset> (named by aria-label) groups the chips; reset its browser chrome.
  list: { display: "flex", flexWrap: "wrap", gap: 8, border: 0, margin: 0, padding: 0, minWidth: 0 } satisfies CSSProperties,
  chip: {
    display: "inline-flex",
    alignItems: "center",
    gap: 8,
    padding: "5px 12px",
    borderRadius: 6,
    fontSize: 13,
    fontWeight: 500,
    border: "1px solid var(--border)",
    background: "transparent",
    color: "var(--text-secondary)",
    cursor: "pointer",
  } satisfies CSSProperties,
  chipOn: {
    border: "1px solid var(--accent)",
    background: "var(--accent-bg)",
    color: "var(--accent-text)",
  } satisfies CSSProperties,
  // Disabled agents are muted; a selected one stays clickable so it can be deselected (A7).
  chipMuted: { opacity: 0.55 } satisfies CSSProperties,
  chipBlocked: { cursor: "not-allowed" } satisfies CSSProperties,
  skipped: { fontSize: 12, fontStyle: "italic", color: "var(--text-muted)" } satisfies CSSProperties,
  notice: { marginTop: 8, fontSize: 13, fontWeight: 600, color: "var(--warn)" } satisfies CSSProperties,
  empty: { display: "flex", alignItems: "center", gap: 10, fontSize: 13, color: "var(--text-muted)" } satisfies CSSProperties,
  link: { color: "var(--accent-text)", fontWeight: 600 } satisfies CSSProperties,
} as const;
