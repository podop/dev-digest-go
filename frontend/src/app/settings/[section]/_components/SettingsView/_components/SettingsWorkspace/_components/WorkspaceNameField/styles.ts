import type { CSSProperties } from "react";

/** Co-located styles for WorkspaceNameField (notes sized as the prototype's field hint). */
export const s = {
  notes: { display: "flex", gap: 12, alignItems: "center", marginTop: 6 } satisfies CSSProperties,
  error: { fontSize: 11.5, fontWeight: 600, color: "var(--crit)" } satisfies CSSProperties,
  saved: { fontSize: 11.5, fontWeight: 600, color: "var(--text-muted)" } satisfies CSSProperties,
} as const;
