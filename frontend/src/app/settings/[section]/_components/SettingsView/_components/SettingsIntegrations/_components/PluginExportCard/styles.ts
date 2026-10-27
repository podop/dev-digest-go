import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for PluginExportCard — the prototype's IntegrationCard "Plugin Export". */
export const s = {
  card: kit.card,
  head: kit.cardHead,
  headIcon: kit.cardIcon,
  headText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  title: { ...kit.cardTitle, margin: 0 } satisfies CSSProperties,
  body: { ...kit.cardDesc, margin: "2px 0 0" } satisfies CSSProperties,
  actions: { display: "flex", gap: 8, flexWrap: "wrap" } satisfies CSSProperties,
  hint: { fontSize: 11.5, color: "var(--text-muted)", marginTop: 8 } satisfies CSSProperties,
  last: {
    display: "flex",
    alignItems: "baseline",
    gap: 4,
    minWidth: 0,
    margin: "10px 0 0",
    fontSize: 11.5,
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  file: {
    minWidth: 0,
    maxWidth: 320,
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  } satisfies CSSProperties,
} as const;
