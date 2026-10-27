import type { CSSProperties } from "react";

/** Co-located styles for the Setup screen frame (centred card, brand, footer) — the prototype's ScreenOnboarding. */
export const s = {
  page: {
    width: "100%",
    minHeight: "calc(100 * var(--app-vh))",
    background: "var(--bg-primary)",
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    padding: "40px 24px",
  } satisfies CSSProperties,
  brand: { display: "flex", alignItems: "center", gap: 10, marginBottom: 28 } satisfies CSSProperties,
  logo: {
    width: 30,
    height: 30,
    borderRadius: 8,
    background: "var(--text-primary)",
    display: "grid",
    placeItems: "center",
  } satisfies CSSProperties,
  brandName: { fontSize: 18, fontWeight: 700, letterSpacing: "-0.02em" } satisfies CSSProperties,
  card: {
    width: 520,
    maxWidth: "100%",
    background: "var(--bg-elevated)",
    border: "1px solid var(--border)",
    borderRadius: 14,
    padding: 32,
    boxShadow: "var(--shadow-modal)",
  } satisfies CSSProperties,
  footer: { fontSize: 12, color: "var(--text-muted)", marginTop: 20 } satisfies CSSProperties,
  skeletons: { display: "flex", flexDirection: "column", gap: 14 } satisfies CSSProperties,
} as const;
