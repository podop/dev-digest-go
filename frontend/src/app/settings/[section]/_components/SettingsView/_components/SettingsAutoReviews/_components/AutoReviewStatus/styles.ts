import type { CSSProperties } from "react";

export type Tone = "ok" | "muted" | "warn";

const COLOR: Record<Tone, string> = { ok: "var(--ok)", muted: "var(--text-muted)", warn: "var(--warn)" };
const HALO: Record<Tone, string> = { ok: "var(--ok-bg)", muted: "transparent", warn: "var(--warn-bg)" };

/** Co-located styles for AutoReviewStatus. */
export const s = {
  line: {
    display: "flex",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 8,
    fontSize: 13,
    color: "var(--text-secondary)",
    marginTop: 14,
  } satisfies CSSProperties,
  muted: { color: "var(--text-muted)" } satisfies CSSProperties,
  link: { color: "var(--accent)", textDecoration: "underline" } satisfies CSSProperties,
  /** Green pulsing dot while active (like the review badge), static otherwise. */
  dot: (tone: Tone): CSSProperties => ({
    width: 7,
    height: 7,
    borderRadius: 99,
    flexShrink: 0,
    background: COLOR[tone],
    boxShadow: tone === "muted" ? "none" : `0 0 0 3px ${HALO[tone]}`,
    animation: tone === "ok" ? "ddpulse 2s ease-in-out infinite" : "none",
  }),
} as const;
