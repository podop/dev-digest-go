import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for KeyField — the prototype's KeyField (screen_settings.jsx). */
export const s = {
  wrap: { marginBottom: 18 } satisfies CSSProperties,
  head: { display: "flex", alignItems: "center", gap: 8, marginBottom: 7 } satisfies CSSProperties,
  label: kit.label,
  optional: {
    fontSize: 11,
    fontWeight: 600,
    color: "var(--text-muted)",
    border: "1px solid var(--border-strong)",
    borderRadius: 4,
    padding: "0 5px",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  docs: kit.headLink,
  foot: { display: "flex", alignItems: "flex-start", gap: 10, marginTop: 7 } satisfies CSSProperties,
  hint: {
    fontSize: 11.5,
    color: "var(--text-muted)",
    display: "inline-flex",
    alignItems: "flex-start",
    gap: 5,
    lineHeight: 1.45,
    minWidth: 0,
  } satisfies CSSProperties,
  hintIcon: { flexShrink: 0, marginTop: 2 } satisfies CSSProperties,
  testButton: { marginLeft: "auto", flexShrink: 0 } satisfies CSSProperties,
  banner: (tone: "ok" | "warn" | "crit"): CSSProperties => ({ ...kit.callout(tone), marginTop: 10, marginBottom: 0 }),
  bannerIcon: (tone: "ok" | "warn" | "crit"): CSSProperties => kit.calloutIcon(tone),
} as const;
