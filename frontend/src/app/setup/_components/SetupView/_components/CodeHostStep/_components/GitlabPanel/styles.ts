import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for the GitLab form of the code-host step (prototype KeyField layout). */
export const s = {
  field: kit.field,
  head: kit.fieldHead,
  label: kit.label,
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
  submit: { marginLeft: "auto", flexShrink: 0 } satisfies CSSProperties,
  result: { alignItems: "flex-start" } satisfies CSSProperties,
  login: { fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
} as const;
