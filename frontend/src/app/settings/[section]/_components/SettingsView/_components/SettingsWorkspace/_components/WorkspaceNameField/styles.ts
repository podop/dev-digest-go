import type { CSSProperties } from "react";

/** Co-located styles for WorkspaceNameField. */
export const s = {
  notes: { display: "flex", gap: 12, alignItems: "center", marginTop: 8, minHeight: 18 } satisfies CSSProperties,
  error: { fontSize: 13, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  saved: { fontSize: 13, fontWeight: 600, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
