import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for GitLabRepos and its rows (prototype "Connected repositories" field). */
export const s = {
  field: kit.field,
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
  item: { listStyle: "none" } satisfies CSSProperties,
  host: { ...kit.rowMeta, overflow: "hidden", textOverflow: "ellipsis", maxWidth: 160 } satisfies CSSProperties,
  badge: {
    display: "inline-flex",
    alignItems: "center",
    gap: 4,
    fontSize: 11.5,
    fontWeight: 600,
    color: "var(--warn)",
    whiteSpace: "nowrap",
    flexShrink: 0,
  } satisfies CSSProperties,
  hint: { fontSize: 11.5, color: "var(--text-muted)", marginTop: 4, paddingLeft: 12, lineHeight: 1.45 } satisfies CSSProperties,
} as const;
