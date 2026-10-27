import type { CSSProperties } from "react";

/** Co-located styles for the provider / host choice: prototype Chips (4px 10px, 12.5px). */
export const s = {
  group: { position: "relative", border: "none", padding: 0, margin: "0 0 18px", minWidth: 0 } satisfies CSSProperties,
  legend: {
    float: "left",
    fontSize: 12.5,
    fontWeight: 600,
    color: "var(--text-secondary)",
    padding: 0,
    marginBottom: 7,
    lineHeight: 1.5,
  } satisfies CSSProperties,
  right: { position: "absolute", top: 0, right: 0, lineHeight: 1.5 } satisfies CSSProperties,
  options: { display: "flex", gap: 7, flexWrap: "wrap", clear: "both" } satisfies CSSProperties,
  option: (on: boolean): CSSProperties => ({
    position: "relative",
    display: "inline-flex",
    alignItems: "center",
    gap: 6,
    padding: "4px 10px",
    borderRadius: 6,
    fontSize: 12.5,
    fontWeight: 500,
    cursor: "pointer",
    transition: "all .12s",
    border: `1px solid ${on ? "var(--accent)" : "var(--border)"}`,
    background: on ? "var(--accent-bg)" : "transparent",
    color: on ? "var(--accent-text)" : "var(--text-secondary)",
  }),
  /** The native radio stays focusable and named; only its box is hidden (the chip shows the state). */
  radio: {
    position: "absolute",
    inset: 0,
    width: "100%",
    height: "100%",
    margin: 0,
    opacity: 0,
    cursor: "pointer",
  } satisfies CSSProperties,
} as const;
