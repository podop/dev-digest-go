import type { CSSProperties } from "react";

/** Co-located styles for AgentChips (chip sizes of the prototype Chip: 4px 10px, 12.5px). */
export const s = {
  // A <fieldset> (named by aria-label) groups the chips; reset its browser chrome.
  list: { display: "flex", flexWrap: "wrap", gap: 7, border: 0, margin: 0, padding: 0, minWidth: 0 } satisfies CSSProperties,
  chip: {
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    padding: "4px 10px",
    borderRadius: 6,
    fontSize: 12.5,
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
  skipped: { fontSize: 11.5, fontStyle: "italic", color: "var(--text-muted)" } satisfies CSSProperties,
  notice: { marginTop: 6, fontSize: 11.5, fontWeight: 600, color: "var(--warn)" } satisfies CSSProperties,
  empty: { display: "flex", alignItems: "center", gap: 10, fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  link: { color: "var(--accent-text)", fontWeight: 600 } satisfies CSSProperties,
} as const;
