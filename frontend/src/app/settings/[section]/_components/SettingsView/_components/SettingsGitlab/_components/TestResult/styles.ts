import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for TestResult: a quiet "Connected as" line, muted facts, and the prototype callout for warnings and failures. */
export const s = {
  wrap: { display: "flex", flexDirection: "column", gap: 6, marginTop: 2 } satisfies CSSProperties,
  facts: {
    listStyle: "none",
    margin: 0,
    padding: 0,
    display: "flex",
    gap: 12,
    flexWrap: "wrap",
    fontSize: 11.5,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  ok: { display: "inline-flex", alignItems: "center", gap: 5, fontSize: 11.5, fontWeight: 600, color: "var(--ok)" } satisfies CSSProperties,
  crit: { ...kit.callout("crit"), marginTop: 4, marginBottom: 0 } satisfies CSSProperties,
  warn: { ...kit.callout("warn"), marginBottom: 0 } satisfies CSSProperties,
  icon: (tone: "crit" | "warn"): CSSProperties => kit.calloutIcon(tone),
} as const;
