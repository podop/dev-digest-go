import type { CSSProperties } from "react";
import { kit } from "@/components/settings-kit";

/** Co-located styles for PluginImportCard — the prototype's IntegrationCard "Plugin Import". */
export const s = {
  card: kit.card,
  head: kit.cardHead,
  headIcon: kit.cardIcon,
  headText: { flex: 1, minWidth: 0 } satisfies CSSProperties,
  title: { ...kit.cardTitle, margin: 0 } satisfies CSSProperties,
  body: { ...kit.cardDesc, margin: "2px 0 0" } satisfies CSSProperties,
  installed: {
    margin: "0 0 12px",
    padding: "11px 14px",
    border: "1px solid rgba(16,185,129,0.25)",
    background: "var(--ok-bg)",
    borderRadius: 8,
    fontSize: 12.5,
    color: "var(--text-secondary)",
    lineHeight: 1.5,
  } satisfies CSSProperties,
  installedTitle: { fontWeight: 700, color: "var(--text-primary)", minWidth: 0 } satisfies CSSProperties,
  name: {
    display: "inline-block",
    maxWidth: "100%",
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
    verticalAlign: "bottom",
  } satisfies CSSProperties,
  hint: { marginTop: 4, fontWeight: 600, color: "var(--text-primary)" } satisfies CSSProperties,
  actions: { display: "flex", justifyContent: "flex-end", marginTop: 10 } satisfies CSSProperties,
} as const;
