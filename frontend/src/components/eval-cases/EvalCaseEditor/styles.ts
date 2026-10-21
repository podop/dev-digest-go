import type { CSSProperties } from "react";

/** Co-located styles for the case editor and its parts. */
export const s = {
  body: { padding: "18px 24px", display: "flex", flexDirection: "column", gap: 18, minWidth: 0 } satisfies CSSProperties,
  // A native <fieldset disabled> disables every control inside while a save is pending.
  fieldset: { border: "none", margin: 0, padding: 0, minWidth: 0, display: "flex", flexDirection: "column", gap: 18 } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 10 } satisfies CSSProperties,
  field: { display: "flex", flexDirection: "column", gap: 6, flex: 1, minWidth: 0 } satisfies CSSProperties,
  label: { fontSize: 13, fontWeight: 600, color: "var(--text-secondary)" } satisfies CSSProperties,
  row: { display: "flex", gap: 12, alignItems: "flex-start" } satisfies CSSProperties,
  heading: { margin: 0, fontSize: 12, fontWeight: 700, letterSpacing: "0.04em", textTransform: "uppercase", color: "var(--text-muted)" } satisfies CSSProperties,
  invalid: { fontSize: 12.5, color: "var(--crit)", overflowWrap: "anywhere" } satisfies CSSProperties,
  hint: { fontSize: 12.5, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  counter: { fontSize: 12, color: "var(--text-muted)", alignSelf: "flex-end" } satisfies CSSProperties,
  counterOver: { fontSize: 12, color: "var(--crit)", alignSelf: "flex-end", fontWeight: 600 } satisfies CSSProperties,
  facts: { display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center", fontSize: 13 } satisfies CSSProperties,
  tag: { fontSize: 11.5, fontWeight: 600, padding: "2px 8px", borderRadius: 999, border: "1px solid var(--border-strong)", color: "var(--text-secondary)" } satisfies CSSProperties,
  preview: { display: "flex", flexDirection: "column", gap: 8, maxHeight: 380, overflow: "auto" } satisfies CSSProperties,
  footer: { display: "flex", alignItems: "center", gap: 8 } satisfies CSSProperties,
  error: { fontSize: 13, color: "var(--crit)", marginRight: "auto", overflowWrap: "anywhere" } satisfies CSSProperties,
  spacer: { flex: 1 } satisfies CSSProperties,
  confirmBody: { margin: 0, padding: "18px 24px", fontSize: 14, lineHeight: 1.5 } satisfies CSSProperties,
  confirmFooter: { display: "flex", justifyContent: "flex-end", gap: 8 } satisfies CSSProperties,
} as const;
