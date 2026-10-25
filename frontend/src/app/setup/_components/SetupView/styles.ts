import type { CSSProperties } from "react";

/** Co-located styles for the Setup screen frame (centred card, brand, footer). */
export const s = {
  page: {
    width: "100%",
    minHeight: "100vh",
    background: "var(--bg-primary)",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "44px 28px",
  } satisfies CSSProperties,
  brand: { display: "flex", alignItems: "center", gap: 12, marginBottom: 32 } satisfies CSSProperties,
  logo: {
    width: 30,
    height: 30,
    borderRadius: 8,
    background: "var(--text-primary)",
    display: "grid",
    placeItems: "center",
  } satisfies CSSProperties,
  brandName: { fontSize: 20, fontWeight: 700, letterSpacing: "-0.02em" } satisfies CSSProperties,
  card: {
    width: 560,
    maxWidth: "100%",
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 14,
    padding: 36,
    boxShadow: "var(--shadow-modal)",
  } satisfies CSSProperties,
  footer: {
    fontSize: 13,
    color: "var(--text-muted)",
    marginTop: 24,
    display: "inline-flex",
    gap: 8,
    alignItems: "center",
  } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 14 } satisfies CSSProperties,
} as const;
