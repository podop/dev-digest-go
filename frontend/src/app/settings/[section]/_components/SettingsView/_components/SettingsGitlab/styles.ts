import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for SettingsGitlab: the connection list as a prototype field, "Add GitLab connection" under it. */
export const s = {
  field: kit.field,
  title: { ...kit.label, display: "block", marginBottom: 7 } satisfies CSSProperties,
  empty: { fontSize: 12.5, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  list: { ...kit.list, listStyle: "none", margin: 0, padding: 0 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 6 } satisfies CSSProperties,
  add: { marginTop: 8 } satisfies CSSProperties,
} as const;
