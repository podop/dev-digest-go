import type { CSSProperties } from "react";

/** Co-located styles for ConfigureRunView. */
export const s = {
  page: { padding: "24px 32px 44px", maxWidth: 820, margin: "0 auto" } satisfies CSSProperties,
  header: { marginBottom: 22 } satisfies CSSProperties,
  h1: { fontSize: 24, fontWeight: 700, letterSpacing: "-0.02em", margin: 0 } satisfies CSSProperties,
  subtitle: { fontSize: 14, color: "var(--text-secondary)", margin: "4px 0 0" } satisfies CSSProperties,
  section: { display: "flex", flexDirection: "column", gap: 10, marginBottom: 24 } satisfies CSSProperties,
  sectionHead: { display: "flex", alignItems: "center", gap: 10 } satisfies CSSProperties,
  h2: {
    flex: 1,
    margin: 0,
    fontSize: 12,
    fontWeight: 600,
    letterSpacing: "0.08em",
    textTransform: "uppercase",
    color: "var(--text-muted)",
  } satisfies CSSProperties,
  prSelect: { maxWidth: 520 } satisfies CSSProperties,
  caption: { fontSize: 13, color: "var(--text-muted)", margin: 0 } satisfies CSSProperties,
  cards: { display: "flex", flexDirection: "column", gap: 8 } satisfies CSSProperties,
  card: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 16,
    padding: "10px 14px",
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 8,
  } satisfies CSSProperties,
  cardLabel: { display: "flex", flexDirection: "column", gap: 2, minWidth: 0 } satisfies CSSProperties,
  cardName: { color: "var(--text-primary)", fontWeight: 500 } satisfies CSSProperties,
  cardDesc: { fontSize: 12.5, color: "var(--text-muted)" } satisfies CSSProperties,
  estimate: { fontSize: 12.5, color: "var(--text-muted)", flexShrink: 0 } satisfies CSSProperties,
  footer: { display: "flex", flexDirection: "column", alignItems: "flex-start", gap: 8 } satisfies CSSProperties,
  footerText: { fontSize: 13, color: "var(--text-secondary)", margin: 0 } satisfies CSSProperties,
} as const;
