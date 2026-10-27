import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for ConnectedRepos (prototype "Connected repositories" FormField). */
export const s = {
  field: kit.field,
  /** The list heading reads as the prototype's field label. */
  title: { ...kit.label, display: "block", marginBottom: 7 } satisfies CSSProperties,
  list: { ...kit.list, listStyle: "none", margin: 0, padding: 0 } satisfies CSSProperties,
  muted: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  add: { marginTop: 8 } satisfies CSSProperties,
  form: {
    border: "1px solid var(--border)",
    borderRadius: 9,
    background: "var(--bg-elevated)",
    padding: 14,
    marginTop: 8,
  } satisfies CSSProperties,
} as const;
